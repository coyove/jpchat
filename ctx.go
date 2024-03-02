package main

import (
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"html/template"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/sirupsen/logrus"
)

//go:embed static/*
var httpStaticPages embed.FS

//go:embed static/assets/*
var httpStaticAssets embed.FS

var uuid = strconv.Itoa(int(time.Now().Unix()))

var httpTemplates = template.Must(template.New("ts").Funcs(template.FuncMap{
	"ServeUUID": func() string {
		return uuid
	},
	"randomChannelSuffix": func(n int) [][2]any {
		res := make([][2]any, n)
		for i := range res {
			tmp := make([]byte, 3)
			rand.Read(tmp)
			if i == n-1 {
				res[i] = [2]any{100, hex.EncodeToString(tmp)}
			} else {
				res[i] = [2]any{i * 100 / n, hex.EncodeToString(tmp)}
			}
		}
		return res
	},
}).ParseFS(httpStaticPages, "static/*.*"))

type Ctx struct {
	*http.Request
	http.ResponseWriter
	IP    net.IP
	Query url.Values
	Uid   string
}

func handle(p string, f func(Ctx)) {
	http.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
		for ua := r.UserAgent(); ; {
			if idx := strings.Index(ua, "Chrome/"); idx > 0 {
				major, _, _ := strings.Cut(ua[idx+7:], ".")
				if i, _ := strconv.Atoi(major); i >= 32 {
					break
				}
			}
			if idx := strings.Index(ua, "Firefox/"); idx > 0 {
				major, _, _ := strings.Cut(ua[idx+8:], ".")
				if i, _ := strconv.Atoi(major); i >= 65 {
					break
				}
			}
			if idx := strings.Index(ua, "Version/"); idx > 0 && strings.Contains(ua, "Safari/") {
				major, _, _ := strings.Cut(ua[idx+8:], ".")
				if i, _ := strconv.Atoi(major); i >= 14 {
					break
				}
			}

			fmt.Fprintf(w, "Browser is too old: %s", ua)
			return
		}

		addr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
		if err != nil {
			fmt.Fprintf(w, "Invalid ip: %s", r.RemoteAddr)
			return
		}

		c := Ctx{
			ResponseWriter: w,
			Request:        r,
			IP:             addr.IP.To16(),
			Query:          r.URL.Query(),
		}

		ck, _ := r.Cookie("uid")
		if ck != nil {
			v, err := base64.URLEncoding.DecodeString(ck.Value)
			if err != nil {
				logrus.Errorf("base64 %q: %v", ck.Value, err)
			} else {
				c.Uid = string(v)
			}
		}
		if c.Uid == "" {
			c.Uid = ipuid(c.IP, r.UserAgent())
		} else {
			c.Uid = sanitizeStrict(c.Uid, 20)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "uid",
			Value:    base64.URLEncoding.EncodeToString([]byte(c.Uid)),
			Expires:  time.Now().AddDate(1, 0, 0),
			HttpOnly: true,
		})

		c.ResponseWriter.Header().Add("Content-Security-Policy", "script-src none")

		f(c)
	})
}

func (c Ctx) Redirect(code int, url string) {
	http.Redirect(c.ResponseWriter, c.Request, url, code)
}

func (c Ctx) Printf(p string, args ...any) {
	fmt.Fprintf(c.ResponseWriter, p, args...)
}

func (c Ctx) Template(name string, arg any) {
	httpTemplates.ExecuteTemplate(c.ResponseWriter, name, arg)
}

func (c Ctx) Write(p []byte) (int, error) {
	return c.ResponseWriter.Write(p)
}

func ipuid(ip net.IP, ua string) string {
	h1 := crc32.ChecksumIEEE(ip.To16())
	h2 := crc32.ChecksumIEEE([]byte(ua))

	// 35bits = 25 + 10
	v := uint64(h1&0x1FFFFFF)<<10 | uint64(h2&0x3FF)

	const base58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	res := make([]byte, 4, 8)
	for i := 0; i < 4; i++ {
		res = append(res, base58[v%58])
		v = v / 58
	}
	copy(res[:4], wordDict[v%uint64(len(wordDict))])
	return *(*string)(unsafe.Pointer(&res))
}
