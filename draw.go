package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const lineHeight = 22

const (
	emojiAdvance = 30
	emojiDim     = 24
	emojiN       = 40
)

var (
	userIconData, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAABHNCSVQICAgIfAhkiAAAAAlwSFlzAAAAbwAAAG8B8aLcQwAAABl0RVh0U29mdHdhcmUAd3d3Lmlua3NjYXBlLm9yZ5vuPBoAAADpSURBVDiNndMtS0RBFMbx3xWECxb7BoMYREwa9voZjBaTwbzRD2PyE1gE2WASbBsFk8UXENG2ICbH4Fy8e2fu1d0DTzlnnv/MnDMjhKAtbOAC73jEKVazazPmdUwRWrpD+R/AecZc66S9fkkaw0yus5YDTHsASS0HuOwBpLVMD1Ywkd7/LDeFIppAURQltnGPQ+zhA2NcYRO3IYTP5ASo8Bx3e8UIa1GjmAt4wu7MGFHiJXPsLj1guQk4mMNca7/5DqqezndFxe8YBwsABk3A2wKAH0/swY75e7A185lwjJs/TF+4xlHt+wZsKfCMyXdZ6AAAAABJRU5ErkJggg==")

	userIcon, _ = png.Decode(bytes.NewReader(userIconData))

	//go:embed embedded/badge.png
	badgeIconData []byte

	badgeIcon, _ = png.Decode(bytes.NewReader(badgeIconData))

	//go:embed embedded/NotoSansMono.otf
	fontData []byte

	drawFont *sfnt.Font

	facePool = &sync.Pool{
		New: func() any {
			f0, _ := opentype.NewFace(drawFont, &opentype.FaceOptions{
				Size:    16,
				DPI:     72,
				Hinting: font.HintingFull,
			})
			return f0
		},
	}

	//go:embed embedded/4-letter.txt
	wordDictData string

	wordDict = func() (res []string) {
		for word := ""; len(wordDictData) > 0; {
			word, wordDictData, _ = strings.Cut(wordDictData, "\n")
			word = strings.ToLower(word)
			res = append(res, word, strings.ToUpper(word[:1])+word[1:])
		}
		return
	}()

	//go:embed embedded/emoji-table
	emojiTableData []byte

	//go:embed embedded/emoji.png
	emojiImageData []byte

	emojiImage, _ = png.Decode(bytes.NewReader(emojiImageData))

	emojiTable = func() map[rune][]emojiSuffix {
		m := map[rune][]emojiSuffix{}
		for i := 0; len(emojiTableData) > 0; i++ {
			n := int(emojiTableData[0])
			emojiTableData = emojiTableData[1:]

			head := binary.BigEndian.Uint32(emojiTableData)
			emojiTableData = emojiTableData[4:]

			suffix := make([]rune, 0, n)
			for ii := 1; ii < n; ii++ {
				r := binary.BigEndian.Uint32(emojiTableData)
				emojiTableData = emojiTableData[4:]

				suffix = append(suffix, rune(r))
			}

			m[rune(head)] = append(m[rune(head)], emojiSuffix{
				text: string(suffix),
				x:    i % emojiN * emojiDim,
				y:    i / emojiN * emojiDim,
			})
		}

		for _, arr := range m {
			sort.Slice(arr, func(i, j int) bool {
				return arr[i].text < arr[j].text
			})
		}
		return m
	}()

	gray = [...]*image.Uniform{
		image.NewUniform(color.RGBA{200, 200, 200, 255}),
		image.NewUniform(color.RGBA{245, 245, 245, 255}),
		image.NewUniform(color.RGBA{180, 180, 180, 255}),
		image.NewUniform(color.RGBA{120, 120, 120, 255}),
	}
	blue   = image.NewUniform(color.RGBA{0, 0, 255, 255})
	blue2  = image.NewUniform(color.RGBA{0, 0, 255, 120})
	wheat  = image.NewUniform(color.RGBA{0xff, 0xec, 0xb3, 255})
	wheat2 = image.NewUniform(color.RGBA{0xee, 0xdb, 0xa2, 255})
)

type emojiSuffix struct {
	text string
	x, y int
}

func DrawStringOmitEmojis(d *font.Drawer, s string) {
	prevC := rune(-1)

	for len(s) > 0 {
		c, cw := utf8.DecodeRuneInString(s)
		s = s[cw:]
		if noDrawRune(c) {
			continue
		}

		if prevC >= 0 {
			d.Dot.X += d.Face.Kern(prevC, c)
		}

		if cand, ok := probeEmoji(c, s); ok {
			s = s[len(cand.text):]
			d.Dot.X += fixed.I(emojiAdvance)
			prevC = -1
			continue
		}

		dr, mask, maskp, advance, _ := d.Face.Glyph(d.Dot, c)
		if !dr.Empty() {
			draw.DrawMask(d.Dst, dr, d.Src, image.Point{}, mask, maskp, draw.Over)
		}
		d.Dot.X += advance
		prevC = c
	}
}

func noDrawRune(r rune) bool {
	return r == '\r' || r == 0x200D || (0xFE00 <= r && r <= 0xFE0F)
}

func probeEmoji(head rune, suffix string) (emojiSuffix, bool) {
	cands := emojiTable[head]
	idx := sort.Search(len(cands), func(i int) bool {
		return cands[i].text >= suffix
	})
	if idx < len(cands) && strings.HasPrefix(suffix, cands[idx].text) {
		return cands[idx], true
	}
	idx--
	if idx >= 0 && strings.HasPrefix(suffix, cands[idx].text) {
		return cands[idx], true
	}
	return emojiSuffix{}, false
}

func makeErrorImage(w, h int, msg string) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	face := facePool.Get().(font.Face)

	defer func() {
		facePool.Put(face)
	}()

	draw.Draw(img, img.Bounds(), image.White, image.Pt(0, 0), draw.Src)
	d := &font.Drawer{Dst: img, Src: image.Black, Face: face}
	tw := d.MeasureString(msg).Round()

	d.Dot.X = fixed.I((w - tw) / 2)
	d.Dot.Y = fixed.I(h - 10)
	d.DrawString(msg)

	out := &bytes.Buffer{}
	jpeg.Encode(out, img, &jpeg.Options{Quality: 80})
	return out.Bytes()
}
