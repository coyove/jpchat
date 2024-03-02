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
	"net/http"
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

type channelNotify struct {
	data     []byte
	kickedBy string
}

type channelOnline struct {
	recv   chan channelNotify
	ip     net.IP
	joined int64
}

type Channel struct {
	Name   string
	Active int64

	mu sync.Mutex

	screen [2]struct {
		waiters     []chan channelNotify
		lastImgData []byte
	}
	onlines     map[string]*channelOnline
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
	r.onlines = map[string]*channelOnline{}
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
	for _, x := range ch.screen {
		sz += len(x.waiters)
	}
	return
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
	w := [2]bytes.Buffer{}
	// jpeg.Encode(w, room.render(400, 960), &jpeg.Options{Quality: q})
	webp.Encode(&w[0], ch.render(0, 400, 960), &webp.Options{Quality: float32(q)})
	webp.Encode(&w[1], ch.render(1, 800, 960), &webp.Options{Quality: float32(q)})
	// w.WriteString(fmt.Sprintf(`
	//     <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 32" width="200" height="32">
	// <g>
	//     <text x="0" y="20">%v</text>
	//     <circle cx="16" cy="16" r="10" stroke-width="2" fill="transparent" stroke="#e57373" />
	//     <path d="M16 11 L16 16" stroke-linecap="round" stroke-width="4" stroke="#e57373" />
	//     <circle cx="16" cy="21" r="2" fill="#e57373" />
	// </g>
	// </svg>
	//     `, time.Now()))

	ch.mu.Lock()
	for i := range ch.screen {
		ch.traffic += int64(w[i].Len())

		ch.screen[i].lastImgData = w[i].Bytes()

		for _, waiter := range ch.screen[i].waiters {
		EXHAUST:
			select {
			case <-waiter:
				goto EXHAUST
			default:
			}

			select {
			case waiter <- channelNotify{data: w[i].Bytes()}:
			default:
			}
		}
	}

	ch.lastElapsed = time.Since(start).Milliseconds()
	ch.mu.Unlock()
}

func (ch *Channel) Join(uid string, w http.ResponseWriter, r *http.Request) {
	addr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if err != nil {
		w.WriteHeader(400)
		return
	}
	ip := addr.IP.To16()

	si, _ := strconv.Atoi(r.URL.Query().Get("screen"))
	if si == 800 {
		si = 1
	} else {
		si = 0
	}

	ch.mu.Lock()
	recv := make(chan channelNotify, 10)
	ch.screen[si].waiters = append(ch.screen[si].waiters, recv)
	if last := ch.screen[si].lastImgData; len(last) > 0 {
		recv <- channelNotify{data: last}
	}

	switching := false
	if state, ok := ch.onlines[uid]; ok {
		if bytes.Equal(ip, state.ip) {
			state.recv <- channelNotify{kickedBy: r.RemoteAddr}
			switching = true
		} else {
			ch.mu.Unlock()
			w.Header().Add("Content-Type", "image/jpeg")
			w.Write(makeErrorImage(400+400*si, 960, fmt.Sprintf("'%s' already exists in this channel", uid)))
			logrus.Infof("[Channel %s] %s from %s failed to kick out %s", ch.Name, uid, ip, state.ip)
			return
		}
	}
	state := &channelOnline{
		ip:     ip,
		recv:   recv,
		joined: time.Now().Unix(),
	}
	ch.onlines[uid] = state
	ch.mu.Unlock()

	if !switching {
		ch.Append(Message{From: uid, Type: MessageJoin})
	}
	ch.Refresh(-1)

	w.Header().Add("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.WriteHeader(200)
	const boundary = "\r\n--frame\r\nContent-Type: image/webp\r\n\r\n"

	peacefullyExit := true
RECV:
	for note := range recv {
		if note.kickedBy != "" {
			logrus.Infof("[Channel %s] %s has switched window, old one lived %vs", ch.Name, uid, time.Now().Unix()-state.joined)
			peacefullyExit = false
			break
		}
		for i := 0; i < 4; i++ {
			w.Write([]byte(boundary))
			if _, err := w.Write(note.data); err != nil {
				logrus.Errorf("stream image data to %v: %v", r.RemoteAddr, err)
				break RECV
			}
		}
	}

	ch.mu.Lock()
	for i := range ch.screen[si].waiters {
		if ch.screen[si].waiters[i] == recv {
			ch.screen[si].waiters = append(ch.screen[si].waiters[:i], ch.screen[si].waiters[i+1:]...)
			break
		}
	}
	if ch.onlines[uid] == state {
		delete(ch.onlines, uid)
	}
	ch.mu.Unlock()

	if peacefullyExit {
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
	lineHeight := 22 // m.Height.Round() * 5 / 4
	barHeight := lineHeight * 3 / 2
	// descent := m.Descent.Round()

	y := h - margin*2 - barHeight

	ch.mu.Lock()
	data := ch.data
	if len(ch.screen[0].waiters)+len(ch.screen[1].waiters) != len(ch.onlines) {
		logrus.Infof("channel head count mismatch: %d + %d <> %d",
			len(ch.screen[0].waiters), len(ch.screen[1].waiters), len(ch.onlines))
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
			1000/float64(ch.lastElapsed), len(ch.screen[si].lastImgData)/1024, float64(ch.traffic)/1024/1024*4)
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
