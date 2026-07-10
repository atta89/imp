// Generator for the default QR logo. Run with:
//
//	go run ./internal/qr/cmd/gen_logo
//
// Writes internal/qr/assets/logo.png. The result is committed; this generator
// is kept around so the asset is reproducible.
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

func main() {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	transparent := color.RGBA{0, 0, 0, 0}
	violet := color.RGBA{0x7c, 0x3a, 0xed, 0xff}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, transparent)
		}
	}

	cx, cy := size/2, size/2
	r := size/2 - 8
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r*r {
				img.Set(x, y, violet)
			}
		}
	}

	barW := size / 8
	barH := size / 3
	barX := cx - barW/2
	barY := cy - barH/3
	for y := barY; y < barY+barH; y++ {
		for x := barX; x < barX+barW; x++ {
			img.Set(x, y, white)
		}
	}

	dotR := size / 12
	dotCY := barY - dotR - size/24
	for y := dotCY - dotR; y <= dotCY+dotR; y++ {
		for x := cx - dotR; x <= cx+dotR; x++ {
			dx, dy := x-cx, y-dotCY
			if dx*dx+dy*dy <= dotR*dotR {
				img.Set(x, y, white)
			}
		}
	}

	const path = "internal/qr/assets/logo.png"
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatalf("encode: %v", err)
	}
	log.Printf("wrote %s (%dx%d)", path, size, size)
}
