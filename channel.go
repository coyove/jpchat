package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/draw"
	"math/rand"
	"net"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chai2010/webp"
	"github.com/sirupsen/logrus"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

const screenHeight = 960

var screenWidths = [...]int{400, 800}

type channelNotify struct {
	data   []byte
	kicked bool
}

type channelOnline struct {
	uid    string
	si     int
	recv   chan channelNotify
	ip     net.IP
	joined int64
}

type Channel struct {
	Name   string
	Active int64

	mu sync.Mutex

	lastImgData [2][]byte
	onlines     map[string][]*channelOnline
	links       []string
	lastElapsed int64
	data        []Message
	traffic     int64
	closed      bool
	autoRefresh *time.Timer
	idctr       uint64
	nameHash    uint32
}

func loadChannel(name string) (*Channel, error) {
	r := &Channel{}
	r.Name = name
	r.onlines = map[string][]*channelOnline{}
	r.nameHash = crc32.ChecksumIEEE([]byte(r.Name))
	r.idctr = rand.Uint64()
	r.autoRefresh = time.AfterFunc(time.Second*10, r.doAutoRefresh)
	r.Refresh(-1)

	tx, err := world.store.Begin(false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if bk := tx.Bucket([]byte("channel-" + name)); bk != nil {
		c := bk.Cursor()
		for k, v := c.First(); len(k) > 0; k, v = c.Next() {
			m := Message{}
			if err := m.Unmarshal(v); err != nil {
				return nil, err
			}
			r.data = append(r.data, m)
		}
	}
	return r, nil
}

func (ch *Channel) doAutoRefresh() {
	if ch.closed {
		return
	}
	if ch.Len() > 0 {
		ch.Refresh(50)
	}
	ch.autoRefresh = time.AfterFunc(time.Second*10, ch.doAutoRefresh)
}

func (ch *Channel) Close() {
	ch.closed = true
}

func (ch *Channel) Len() (sz int) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return len(ch.onlines)
}

func (ch *Channel) Append(e Message) error {
	ch.mu.Lock()

	ch.Active = time.Now().Unix()
	ch.idctr++
	e.ID = uint64(ch.Active-16e8)<<31 | uint64(ch.nameHash&0x7FFF)<<16 | (ch.idctr & 0xFFFF)
	e.UnixTime = time.Now().Unix()

	ch.data = append(ch.data, e)
	if len(ch.data) > 50 {
		ch.data = ch.data[1:]
	}

	ch.mu.Unlock()

	ch.autoRefresh.Reset(time.Second * 10)

	switch e.Type {
	case MessageJoin, MessageLeave:
		return nil
	}

	tx, err := world.store.Begin(true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bk, _ := tx.CreateBucketIfNotExists([]byte("channel-" + ch.Name))
	bk.Put(binary.BigEndian.AppendUint64(nil, e.ID), e.Marshal())
	if n, _ := bk.NextSequence(); n > 50 {
		k, _ := bk.Cursor().First()
		bk.Delete(k)
	}

	bk, _ = tx.CreateBucketIfNotExists([]byte("channel"))
	bkSort, _ := tx.CreateBucketIfNotExists([]byte("channelsort"))

	namebuf := []byte(ch.Name)
	timebuf := binary.BigEndian.AppendUint64(nil, uint64(ch.Active))
	if old := bk.Get(namebuf); len(old) > 0 {
		bkSort.Delete(append(old, namebuf...))
	}
	bkSort.Put(append(timebuf, namebuf...), nil)
	if created, _ := bk.TestPut(namebuf, timebuf); created {
		bk.NextSequence()
	}

	return tx.Commit()
}

func (ch *Channel) Refresh(q int) {
	if q == -1 {
		q = 50
	}

	start := time.Now()
	var outs [][]byte
	for i, w := range screenWidths {
		out := bytes.Buffer{}
		webp.Encode(&out, ch.render(i, w, screenHeight), &webp.Options{Quality: float32(q)})
		outs = append(outs, out.Bytes())
	}

	ch.mu.Lock()
	for i, data := range outs {
		ch.traffic += int64(len(data))
		ch.lastImgData[i] = data
	}

	for _, arr := range ch.onlines {
		for _, waiter := range arr {
		EXHAUST:
			select {
			case <-waiter.recv:
				goto EXHAUST
			default:
			}

			select {
			case waiter.recv <- channelNotify{data: outs[waiter.si]}:
			default:
			}
		}
	}

	ch.lastElapsed = time.Since(start).Milliseconds()
	ch.mu.Unlock()
}

func (ch *Channel) Join(uid string, c Ctx) {
	si, _ := strconv.Atoi(c.Query.Get("screen"))
	if si == 800 {
		si = 1
	} else {
		si = 0
	}

	ch.mu.Lock()
	switching := false
	if arr, ok := ch.onlines[uid]; ok {
		if bytes.Equal(c.IP, arr[len(arr)-1].ip) {
			for _, oldState := range arr {
				oldState.recv <- channelNotify{
					kicked: true,
					data:   makeErrorImage(screenWidths[oldState.si], screenHeight, "Chat has been opened elsewhere"),
				}
			}
			switching = true
		} else {
			ch.mu.Unlock()
			c.ResponseWriter.Header().Add("Content-Type", "image/jpeg")
			c.Write(makeErrorImage(400+400*si, 960, fmt.Sprintf("'%s' already exists in this channel", uid)))
			logrus.Infof("[Channel %s] %s can't join due to same nickname %s", ch.Name, c.RemoteAddr, uid)
			return
		}
	}
	state := &channelOnline{
		uid:    uid,
		ip:     c.IP,
		si:     si,
		recv:   make(chan channelNotify, 10),
		joined: time.Now().Unix(),
	}
	ch.onlines[uid] = append(ch.onlines[uid], state)
	if last := ch.lastImgData[si]; len(last) > 0 {
		state.recv <- channelNotify{data: last}
	}
	ch.mu.Unlock()

	if !switching {
		ch.Append(Message{From: uid, Type: MessageJoin})
	}
	ch.Refresh(-1)

	c.ResponseWriter.Header().Add("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	c.WriteHeader(200)
	const boundary = "\r\n--frame\r\nContent-Type: image/webp\r\n\r\n"

	var note channelNotify
RECV:
	for note = range state.recv {
		for i := 0; i < 4; i++ {
			c.Write([]byte(boundary))
			if _, err := c.Write(note.data); err != nil {
				logrus.Errorf("stream image data to %v: %v", c.RemoteAddr, err)
				break RECV
			}
		}
		if note.kicked {
			logrus.Infof("[Channel %s] %s has switched window, old one lived %vs", ch.Name, uid, time.Now().Unix()-state.joined)
			break
		}
	}

	ch.mu.Lock()
	for i, w := range ch.onlines[uid] {
		if w == state {
			ch.onlines[uid] = append(ch.onlines[uid][:i], ch.onlines[uid][i+1:]...)
			if len(ch.onlines[uid]) == 0 {
				delete(ch.onlines, uid)
			}
			break
		}
	}
	ch.mu.Unlock()

	if !note.kicked {
		ch.Append(Message{From: uid, Type: MessageLeave})
	}
	ch.Refresh(-1)
}

func (ch *Channel) render(si, w, h int) (img *image.RGBA) {
	img = image.NewRGBA(image.Rect(0, 0, w, h))
	face := facePool.Get().(font.Face)

	defer func() {
		if r := recover(); r != nil {
			logrus.Errorf("%v: %s", r, debug.Stack())
		}
		facePool.Put(face)
	}()

	draw.Draw(img, img.Bounds(), image.White, image.Pt(0, 0), draw.Src)

	d := &font.Drawer{Dst: img, Src: image.Black, Face: face}
	dg := &font.Drawer{Dst: img, Src: gray[3], Face: face}
	du := &font.Drawer{Dst: img, Src: blue, Face: face}

	const margin = 4
	const contentLeft = margin * 2.5

	// m := face.Metrics()
	barHeight := lineHeight * 3 / 2
	// descent := m.Descent.Round()

	y := h - margin*2 - barHeight

	ch.mu.Lock()
	data := ch.data
	for uid, arr := range ch.onlines {
		if len(arr) != 1 {
			logrus.Infof("[Channel %s] multiple nickname %s", ch.Name, uid)
		}
	}
	ch.mu.Unlock()

	type elem struct {
		text string
		gray bool
	}

	type overlay struct {
		typ  int
		x    fixed.Int26_6
		y    int
		srcX int
		srcY int
	}

	links := []string{}
	for i := len(data) - 1; i >= 0; i-- {
		message := data[i]

		switch message.Type {
		case MessageJoin, MessageLeave:
			var msg string
			if message.Type == MessageJoin {
				msg = time.Unix(message.UnixTime, 0).Format(" joined at 15:04")
			} else {
				msg = time.Unix(message.UnixTime, 0).Format(" left at 15:04")
			}

			du.Dot.X = fixed.I((w - du.MeasureString(message.From).Round() - d.MeasureString(msg).Round()) / 2)
			du.Dot.Y = fixed.I(y)
			du.DrawString(message.From)
			d.Dot = du.Dot
			d.DrawString(msg)
			y -= lineHeight * 5 / 4
			continue
		}

		var lines []elem
		var overlays []overlay
		for msg := strings.Replace(message.Text, "\t", "  ", -1); len(msg) > 0; {
			var line string
			line, msg, _ = strings.Cut(msg, "\n")

			prevC := rune(-1)
			prevI := 0
			x := fixed.I(contentLeft)
			for i := 0; i < len(line); {
				if len(links) < 16 && (strings.HasPrefix(line[i:], "http://") || strings.HasPrefix(line[i:], "https://")) {
					overlays = append(overlays, overlay{
						typ:  'l',
						x:    x,
						y:    len(lines),
						srcX: len(links),
					})
					idx := strings.IndexAny(line[i:], " \t\r\n")
					if idx == -1 {
						idx = len(line[i:])
					}
					links = append(links, line[i:i+idx])
				}

				c, cw := utf8.DecodeRuneInString(line[i:])
				i += cw

				if noDrawRune(c) {
					continue
				}

				if prevC >= 0 {
					x += d.Face.Kern(prevC, c)
				}

				cand, isEmoji := probeEmoji(c, line[i:])
				if isEmoji {
					overlays = append(overlays, overlay{
						typ:  'e',
						x:    x,
						y:    len(lines),
						srcX: cand.x,
						srcY: cand.y,
					})
					x += fixed.I(emojiAdvance)
					i += len(cand.text)
				} else {
					advance, _ := d.Face.GlyphAdvance(c)
					x += advance
				}

				if x >= fixed.I(w-margin*6) {
					x = fixed.I(contentLeft)
					lines = append(lines, elem{text: line[prevI:i]})
					prevI = i
					prevC = -1
				} else if isEmoji {
					prevC = -1
				} else {
					prevC = c
				}
			}
			if prevI < len(line) {
				lines = append(lines, elem{text: line[prevI:]})
			}
		}

		if len(lines) >= 10 {
			lines = append(lines, elem{text: "\u2191 " + message.From, gray: true})
		}

		y -= len(lines)*lineHeight - lineHeight

		if i%2 == 0 {
			draw.Draw(img, image.Rect(0, y-lineHeight*2, w, y+len(lines)*lineHeight-lineHeight*5/8), gray[1], image.ZP, draw.Src)
		}

		for i, el := range lines {
			if el.gray {
				dg.Dot.X = fixed.I(w) - dg.MeasureString(el.text) - fixed.I(contentLeft)
				dg.Dot.Y = fixed.I(y + i*lineHeight)
				dg.DrawString(el.text)
			} else {
				d.Dot.X = fixed.I(contentLeft)
				d.Dot.Y = fixed.I(y + i*lineHeight)
				DrawStringOmitEmojis(d, el.text)
			}
		}

		for _, el := range overlays {
			xx := el.x.Round()
			yy := y + el.y*lineHeight - lineHeight
			switch el.typ {
			case 'l':
				msg := strconv.FormatInt(int64(el.srcX), 16)
				draw.Draw(img, image.Rect(xx, yy+4,
					xx+w, yy+h), badgeIcon, image.ZP, draw.Over)
				d.Src = image.White
				d.Dot.X = fixed.I(xx + (badgeIcon.Bounds().Dx()-d.MeasureString(msg).Round())/2)
				d.Dot.Y = fixed.I(yy + lineHeight)
				d.DrawString(msg)
				d.Src = image.Black
			case 'e':
				xx = xx + (emojiAdvance-emojiDim)/2
				yy = yy + 4
				draw.Draw(img, image.Rect(xx, yy, xx+emojiDim, yy+emojiDim),
					emojiImage, image.Pt(el.srcX, el.srcY), draw.Over)
			}
		}

		y -= lineHeight

		du.Dot.X = fixed.I(margin)
		du.Dot.Y = fixed.I(y)
		du.DrawString(message.From)

		d.Dot.X = fixed.I(margin + du.MeasureString(message.From).Round() + margin*2)
		d.Dot.Y = fixed.I(y)
		t := time.Unix(message.UnixTime, 0)
		if t.YearDay() != time.Now().YearDay() {
			d.DrawString(t.Format("01-02 15:04"))
		} else {
			d.DrawString(t.Format("15:04:05"))
		}

		y -= lineHeight * 5 / 4

		if y < 0 {
			break
		}
	}

	{
		// Draw bottom bar.
		margin := margin * 3 / 2
		draw.Draw(img, image.Rect(0, h-barHeight, w, h), wheat, image.Pt(0, 0), draw.Src)
		d.Dot.Y = fixed.I(h - (barHeight-16)/2 - 3)

		n := strconv.Itoa(ch.Len())
		nx := margin + 16 + margin + d.MeasureString(n).Round() + margin
		draw.Draw(img, image.Rect(0, h-barHeight, nx, h), gray[0], image.Pt(0, 0), draw.Src)
		draw.Draw(img, image.Rect(margin, h-barHeight+(barHeight-16)/2, w, h), userIcon, image.Pt(0, 0), draw.Over)
		d.Dot.X = fixed.I(margin + 16 + margin)
		d.DrawString(n)

		ts := time.Now().Format("15:04")
		d.Dot.X = fixed.I(nx + margin*2)
		d.DrawString(ts)

		traffic := fmt.Sprintf("%.1ffps %d:%.2fM",
			1000/float64(ch.lastElapsed), len(ch.lastImgData[si])/1024, float64(ch.traffic)/1024/1024*4)
		tw := d.MeasureString(traffic)
		d.Dot.X = fixed.I(w-contentLeft) - tw
		d.DrawString(traffic)

		draw.Draw(img, image.Rect(0, h-2, nx, h), gray[2], image.Pt(0, 0), draw.Src)
		draw.Draw(img, image.Rect(nx, h-2, w, h), wheat2, image.Pt(0, 0), draw.Src)
	}

	ch.mu.Lock()
	ch.links = links
	ch.mu.Unlock()
	return img
}
