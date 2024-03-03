package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"

	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

func main() {
	os.MkdirAll("embedded/icons", 0777)

	var p string
	flag.StringVar(&p, "f", "", "")
	flag.Parse()
	buf, _ := os.ReadFile(p)

	f, _ := truetype.Parse(buf)

	const DIM = 64
	face := truetype.NewFace(f, &truetype.Options{
		Size:    DIM,
		DPI:     72,
		Hinting: font.HintingFull,
	})

	for i := 0; i < 65536; i++ {
		if f.Index(rune(i)) > 0 {
			b, _, _ := face.GlyphBounds(rune(i))
			img := image.NewRGBA(image.Rect(0, 0, b.Max.X.Round(), DIM))
			d := &font.Drawer{
				Face: face,
				Dst:  img,
				Src:  image.Black,
			}
			d.Dot.Y = fixed.I(DIM) - face.Metrics().Descent
			d.DrawString(string(rune(i)))
			out, _ := os.Create(fmt.Sprintf("static/assets/%x.png", i))
			png.Encode(out, img)
		}
	}
}
