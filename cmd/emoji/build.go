package main

import (
	"archive/zip"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"strings"

	"github.com/nfnt/resize"
)

const (
	emojiAdvance = 30
	emojiDim     = 24
	emojiN       = 40
)

func main() {
	var zf string
	flag.StringVar(&zf, "d", "", "")
	flag.Parse()

	rd, _ := zip.OpenReader(zf)
	defer rd.Close()

	canvas := image.NewRGBA(image.Rect(0, 0, emojiDim*emojiN, emojiDim*emojiN))
	i := 0
	var seq []byte
	dedup := map[string]bool{}
	for _, file := range rd.File {
		tmp := file.Name
		if file.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(tmp, "emojis-main/google/") { //  && !strings.HasPrefix(tmp, "emojis-main/apple/") {
			continue
		}
		tmp = strings.TrimPrefix(tmp, "emojis-main/google/")
		tmp = strings.TrimPrefix(tmp, "emojis-main/apple/")
		tmp = strings.TrimSuffix(tmp, ".png")
		if dedup[tmp] {
			continue
		}

		dedup[tmp] = true

		parts := []rune(tmp)
		seq = append(seq, byte(len(parts)))
		for _, v := range parts {
			seq = binary.BigEndian.AppendUint32(seq, uint32(v))
		}

		rd, _ := file.Open()
		img, _ := png.Decode(rd)
		rd.Close()

		out := resize.Resize(emojiDim, emojiDim, img, resize.Bicubic)
		x, y := i%emojiN, i/emojiN
		draw.Draw(canvas, image.Rect(x*emojiDim, y*emojiDim, x*emojiDim+emojiDim, y*emojiDim+emojiDim), out, image.ZP, draw.Over)
		i++
		fmt.Println(i, parts)
	}
	out, _ := os.Create("emoji.png")
	png.Encode(out, canvas)

	os.WriteFile("emoji-table", seq, 0644)
}
