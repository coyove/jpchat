package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"io"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coyove/bbolt"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/image/font/opentype"
	"gopkg.in/natefinch/lumberjack.v2"
)

var world struct {
	sync.Mutex
	channels   map[string]*Channel
	totalUsers atomic.Int64
	store      *bbolt.DB
}

var (
	domain        = flag.String("d", "", "production")
	onlineKey     = flag.String("k", "coyove", "production key")
	onlineKeyhash string
)

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
	onlineKeyhash = hmacHex(*onlineKey)

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

	handle("/", handleIndex)
	handle("/~send/", handleSend)
	handle("/~ping/", handlePing)
	handle("/~link/", handleLink)
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
		case strings.HasSuffix(p, ".svg"):
			c.ResponseWriter.Header().Add("Content-Type", "image/svg+xml")
		}
		c.ResponseWriter.Header().Add("Cache-Control", "public, max-age=8640000")

		buf, _ := httpStaticAssets.ReadFile("static/assets/" + p)
		c.ResponseWriter.Write(buf)
	})

	pkey := hex.EncodeToString(randBytes(10))
	http.Handle("/~"+pkey+"/debug/pprof/", http.StripPrefix("/~"+pkey, http.HandlerFunc(pprof.Index)))
	handle("/~stats", func(c Ctx) {
		if !c.isAdmin() {
			c.WriteHeader(400)
			return
		}

		m := runtime.MemStats{}
		runtime.ReadMemStats(&m)

		stats := world.store.Stats()

		out, _ := exec.Command("uptime").Output()

		c.ResponseWriter.Header().Add("Content-Type", "text/html")
		c.Printf(`
<p>load: %s</p>
<p>mem: %.1fM</p>
<p>disk: %.1fM</p>
<p>freepage: %d</p>
<a href="/~%s/debug/pprof/">pprof</a>
        `,
			out,
			float64(m.HeapInuse)/1024/1024,
			float64(world.store.Size())/1024/1024,
			stats.FreePageN+stats.PendingPageN,
			pkey,
		)
	})

	addr := ":8888"
	srv := http.Server{
		Addr:     addr,
		ErrorLog: log.New(lf, "", 0),
	}

	if *domain != "" {
		autocertManager := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*domain),
			Cache:      &certCache{},
		}
		go func() {
			logrus.Infof("serving autocert for %s", *domain)
			logrus.Fatal(http.ListenAndServe(":http", autocertManager.HTTPHandler(nil)))
		}()

		srv.Addr = ":https"
		srv.TLSConfig = &tls.Config{
			GetCertificate: autocertManager.GetCertificate,
			NextProtos:     []string{"http/0.9", "http/1.0", "http/1.1", acme.ALPNProto},
		}
		logrus.Infof("serving https")
		logrus.Fatal(srv.ListenAndServeTLS("", ""))
	} else {
		logrus.Infof("serving at %v", addr)
		logrus.Fatal(srv.ListenAndServe())
	}
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

type certCache struct{}

func (cc *certCache) Get(ctx context.Context, key string) ([]byte, error) {
	tx, err := world.store.Begin(false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	bk := tx.Bucket([]byte("cert"))
	if bk == nil {
		return nil, autocert.ErrCacheMiss
	}
	v := bk.Get([]byte(key))
	if len(v) == 0 {
		return nil, autocert.ErrCacheMiss
	}
	return v, nil
}

func (cc *certCache) Put(ctx context.Context, key string, data []byte) error {
	tx, err := world.store.Begin(true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bk, _ := tx.CreateBucketIfNotExists([]byte("cert"))
	bk.Put([]byte(key), data)
	return tx.Commit()
}

func (cc *certCache) Delete(ctx context.Context, key string) error {
	tx, err := world.store.Begin(true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bk, _ := tx.CreateBucketIfNotExists([]byte("cert"))
	bk.Delete([]byte(key))
	return tx.Commit()
}
