package main

import (
	"bytes"
	"flag"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coyove/bbolt"
	"github.com/sirupsen/logrus"
	"golang.org/x/image/font/opentype"
	"gopkg.in/natefinch/lumberjack.v2"
)

var world struct {
	sync.Mutex
	channels   map[string]*Channel
	totalUsers atomic.Int64
	store      *bbolt.DB
}

func purgeWorld() {
	world.Lock()
	world.totalUsers.Store(0)
	for k, ch := range world.channels {
		if ch.Len() == 0 {
			ch.Close()
			delete(world.channels, k)
			logrus.Infof("channel %s is empty and purged", ch.Name)
		} else {
			world.totalUsers.Add(int64(ch.Len()))
		}
	}
	world.Unlock()
	time.AfterFunc(time.Minute, purgeWorld)
}

func findChannel(name string) (*Channel, bool) {
	world.Lock()
	ch, ok := world.channels[name]
	world.Unlock()
	return ch, ok
}

func main() {
	flag.Parse()
	lf := &logFormatter{io.MultiWriter(os.Stdout, &lumberjack.Logger{
		Filename:   "logs/chat.log",
		MaxSize:    20,
		MaxBackups: 10,
		MaxAge:     7,
		Compress:   true,
	})}
	logrus.SetFormatter(lf)
	logrus.SetOutput(lf.out)
	logrus.SetReportCaller(true)

	var err error
	drawFont, err = opentype.Parse(fontData)
	if err != nil {
		logrus.Fatal(err)
	}

	world.channels = map[string]*Channel{}
	world.store, err = bbolt.Open("chat.db", 0644, &bbolt.Options{
		FreelistType: bbolt.FreelistMapType,
	})
	if err != nil {
		logrus.Fatal(err)
	}

	purgeWorld()

	// room := NewChannel("test")
	// world.channels[room.Name] = room
	// go func() {
	// 	for true {
	// 		msg := "Α	Β	Γ	Δ	Ε	Ζ	Η	Θ	Ι	Κ	Λ	Μ	Ν	Ξ	Ο	Π	Ρ	Σ	Τ	Υ	Φ	Χ	Ψ"
	// 		for i, c := 0, rand.Intn(2)+2; i < c; i++ {
	// 			msg += "\n" + strconv.Itoa(i) + " "
	// 			for ii, c := 0, rand.Intn(20)+20; ii < c; ii++ {
	// 				msg += string(rune(rand.Intn(60000)) + 100)
	// 			}
	// 		}
	// 		if rand.Intn(3) == 1 {
	// 			msg = "1"
	// 		}
	// 		room.Append(Message{
	// 			From:     "coyove",
	// 			UnixTime: time.Now().Unix(),
	// 			Text:     msg,
	// 		})
	// 		room.Refresh(-1)
	// 		time.Sleep(time.Second * 2)
	// 	}
	// }()

	handle("/", handleIndex)

	handle("/~send/", handleSend)

	handle("/~ping/", func(c Ctx) {
		name := sanitizeChannelName(c.URL.Path[7:])
		if ch, ok := findChannel(name); ok {
			ch.mu.Lock()
			if arr := ch.onlines[c.Uid]; len(arr) > 0 {
				u := arr[len(arr)-1]
				u.timeout.Reset(pingTimeout)
			}
			ch.mu.Unlock()
		}
		c.WriteHeader(200)
		c.Write([]byte(`<html><meta http-equiv="refresh" content="10">`))
	})

	handle("/~link/", func(c Ctx) {
		name := sanitizeChannelName(c.URL.Path[7:])
		idx, _ := strconv.ParseInt(c.Query.Get("link"), 16, 64)

		var links []string
		var link string

		if ch, ok := findChannel(name); ok {
			ch.mu.Lock()
			links = ch.links
			ch.mu.Unlock()
			if 0 <= idx && int(idx) < len(ch.links) {
				link = links[idx]
			}
		}

		if link == "" {
			c.ResponseWriter.Header().Add("Content-Type", "text/html")
			if len(links) > 0 {
				c.Printf(`
            <p>
            Link %x doesn't exist in the current channel.<br>
            Here are currently available links on screen:<br>
            `, idx)
				for i := 0; i < 16 && i < len(links); i++ {
					u := html.EscapeString(links[i])
					c.Printf(`%x: <a href="%s">%s</a><br>`, i, u, u)
				}
			} else {
				c.Printf(`<p>This channel doesn't have any links. 
                New link appeared on chat screen will be assigned a tag, so you know which to open.
                </p>`)
			}
		} else {
			c.Redirect(302, link)
		}
	})

	handle("/~stream", func(c Ctx) {
		name := c.Query.Get("name")
		if name == "" {
			c.WriteHeader(404)
			return
		}

		world.Lock()
		ch, ok := world.channels[name]
		if !ok {
			var err error
			ch, err = loadChannel(name)
			if err != nil {
				world.Unlock()
				c.WriteHeader(500)
				logrus.Errorf("load channel: %v", err)
				return
			}
			world.channels[name] = ch
		}
		world.Unlock()

		ch.Join(c.Uid, c)
	})

	handle("/~static/", func(c Ctx) {
		p := strings.TrimPrefix(c.URL.Path, "/~static/")
		switch {
		case strings.HasSuffix(p, ".css"):
			c.ResponseWriter.Header().Add("Content-Type", "text/css")
		case strings.HasSuffix(p, ".png"):
			c.ResponseWriter.Header().Add("Content-Type", "image/png")
		}
		c.ResponseWriter.Header().Add("Cache-Control", "public, max-age=8640000")

		buf, _ := httpStaticAssets.ReadFile("static/assets/" + p)
		c.ResponseWriter.Write(buf)
	})

	addr := ":8888"
	srv := http.Server{
		Addr: addr,
	}
	logrus.Infof("serving at %v", addr)
	logrus.Fatal(srv.ListenAndServe())
}

type logFormatter struct {
	out io.Writer
}

func (f *logFormatter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("not configured in HostWhitelist")) {
		return len(p), nil
	}
	if bytes.Contains(p, []byte("TLS handshake error")) && bytes.Contains(p, []byte("EOF")) {
		return len(p), nil
	}
	if bytes.Contains(p, []byte("acme/autocert: missing server name")) {
		return len(p), nil
	}
	f.out.Write([]byte(time.Now().UTC().Format("ERR\t2006-01-02T15:04:05.000\tgohttp\t")))
	return f.out.Write(p)
}

func (f *logFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	buf := bytes.Buffer{}
	if entry.Level <= logrus.ErrorLevel {
		buf.WriteString("ERR")
	} else {
		buf.WriteString("INFO")
	}
	buf.WriteString("\t")
	buf.WriteString(entry.Time.UTC().Format("2006-01-02T15:04:05.000\t"))
	if entry.Caller == nil {
		buf.WriteString("internal")
	} else {
		buf.WriteString(filepath.Base(entry.Caller.File))
		buf.WriteString(":")
		buf.WriteString(strconv.Itoa(entry.Caller.Line))
	}
	buf.WriteString("\t")
	buf.WriteString(entry.Message)
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
