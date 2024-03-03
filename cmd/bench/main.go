package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

var u, ch, admin string

func main() {
	flag.StringVar(&u, "url", "", "")
	flag.StringVar(&ch, "ch", "", "")
	flag.StringVar(&admin, "admin", "", "")
	flag.Parse()

	if u == "" || ch == "" {
		return
	}

	for r := 0; ; r++ {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go send(&wg)
		}
		wg.Wait()
		fmt.Println("round", r)
	}
}

func send(wg *sync.WaitGroup) {
	defer wg.Done()

	req, _ := http.NewRequest("GET", u+"/~send/"+ch, nil)
	req.Header.Add("User-Agent", "a Chrome/100.0.0.0")
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	tok := regexp.MustCompile(`name=token value=(.+?)>`).FindSubmatch(body)[1]
	// fmt.Println(string(tok))

	msg := ""
	for i, n := 0, rand.Intn(30)+30; i < n; i++ {
		msg += string(rune(rand.Intn(65536)))
	}

	data := url.Values{}
	data.Set("channel", ch)
	data.Set("token", string(tok))
	data.Set("msg", msg)

	req, _ = http.NewRequest("POST", u+"/~send/"+ch, strings.NewReader(data.Encode()))
	req.Header.Add("User-Agent", "a Chrome/100.0.0.0")
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Cookie", "admin="+hmacHex(admin))
	resp, _ = http.DefaultClient.Do(req)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
}

func hmacHex(v string) string {
	h := hmac.New(sha1.New, []byte(admin))
	h.Write([]byte(v))
	return hex.EncodeToString(h.Sum(nil))
}
