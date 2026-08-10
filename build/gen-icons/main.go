// Command gen-icons is a one-time, throwaway build tool — NOT part of the
// nexler CLI itself, hence its own separate go.mod (keeps oksvg/rasterx
// out of nexler's own dependency graph entirely). It rasterizes an SVG
// source (installer/nexler-icon.svg — Nexler's own app icon, distinct
// from internal/scaffold/templates/templates/static/logo.svg, the
// generic starter logo scaffolded into apps built *with* nexler) into
// either:
//
//   - installer/windows/nexler.ico, a multi-resolution ICO the Inno Setup
//     installer uses as its SetupIconFile/app icon, or
//   - a single high-resolution master PNG, which installer/macos/
//     build-pkg.sh resizes (via macOS's own `sips`, which reliably
//     handles PNG->PNG resizing) down to each size an .icns needs before
//     packing with `iconutil`. SVG decoding support in `sips` itself is
//     inconsistent across macOS versions and can't be verified from this
//     (non-macOS) dev machine, so the macOS build step deliberately never
//     asks `sips` to read the SVG directly — only this already-verified
//     Go rasterizer does that, once, for both platforms' icon needs.
//
// Usage:
//
//	go run . <logo.svg> <output.ico>
//	go run . <logo.svg> <output.png> <size>
//	go run . <logo.svg> <output.png> <width>x<height>
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// percentWidthHeightRe matches a width="…%" or height="…%" attribute.
// oksvg's <svg> attribute parser (draw.go's svgF) walks attributes in
// document order and bails out of the whole element — leaving ViewBox at
// its zeroed default — the moment parseFloat fails on any attribute, not
// just viewBox itself. logo.svg's width="100%" sits before its viewBox
// attribute in source order, so an unstripped "100%" corrupts ViewBox to
// (0,0,0,0), which then makes SetTarget divide by zero and silently
// render nothing (an all-transparent frame, not a crash — easy to miss
// without visually checking the output). Only viewBox actually matters
// for rasterizing at an arbitrary output size, so stripping width/height
// entirely is safe here.
var percentWidthHeightRe = regexp.MustCompile(`\s+(?:width|height)="[^"]*%[^"]*"`)

func stripPercentWidthHeight(svg []byte) []byte {
	return percentWidthHeightRe.ReplaceAll(svg, nil)
}

// icoSizes are the resolutions bundled into the .ico — the standard
// Windows shell set (16/32/48 for menus/Explorer list views, 256 for
// Explorer's "Extra large icons" view and the taskbar/Start Menu tile).
var icoSizes = []int{16, 32, 48, 256}

func main() {
	if len(os.Args) < 3 || len(os.Args) > 4 {
		fmt.Fprintln(os.Stderr, "usage: gen-icons <logo.svg> <output.ico>")
		fmt.Fprintln(os.Stderr, "       gen-icons <logo.svg> <output.png> <size>")
		os.Exit(1)
	}
	svgPath, outPath := os.Args[1], os.Args[2]

	svgData, err := os.ReadFile(svgPath)
	if err != nil {
		fatalf("reading %s: %v", svgPath, err)
	}
	svgData = stripPercentWidthHeight(svgData)
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svgData), oksvg.WarnErrorMode)
	if err != nil {
		fatalf("parsing %s: %v", svgPath, err)
	}
	if icon.ViewBox.W == 0 || icon.ViewBox.H == 0 {
		fatalf("%s has no usable viewBox after parsing — icon.ViewBox is %+v", svgPath, icon.ViewBox)
	}

	if strings.HasSuffix(outPath, ".png") {
		if len(os.Args) != 4 {
			fatalf("a .png output needs a <size> or <width>x<height> argument, e.g.: gen-icons %s %s 1024", svgPath, outPath)
		}
		width, height, err := parseSize(os.Args[3])
		if err != nil {
			fatalf("invalid size %q: %v", os.Args[3], err)
		}
		pngData, err := rasterizeToPNG(icon, width, height)
		if err != nil {
			fatalf("rasterizing %dx%d: %v", width, height, err)
		}
		if err := os.WriteFile(outPath, pngData, 0o644); err != nil {
			fatalf("writing %s: %v", outPath, err)
		}
		fmt.Printf("Wrote %s (%dx%d)\n", outPath, width, height)
		return
	}

	var pngFrames [][]byte
	for _, size := range icoSizes {
		png, err := rasterizeToPNG(icon, size, size)
		if err != nil {
			fatalf("rasterizing %dx%d: %v", size, size, err)
		}
		pngFrames = append(pngFrames, png)
	}

	icoData, err := encodeICO(icoSizes, pngFrames)
	if err != nil {
		fatalf("encoding ICO: %v", err)
	}
	if err := os.WriteFile(outPath, icoData, 0o644); err != nil {
		fatalf("writing %s: %v", outPath, err)
	}
	fmt.Printf("Wrote %s (%d frame(s): %v)\n", outPath, len(icoSizes), icoSizes)
}

// rasterizeToPNG renders icon into a width x height RGBA canvas
// (transparent background) and PNG-encodes it. Modern Windows accepts
// PNG-compressed frames directly inside an ICO container at any size —
// no legacy BMP/DIB fallback needed for a Windows 10/11-targeted
// installer. width/height need not match the source SVG's own aspect
// ratio — SetTarget scales width/height independently, so a source
// authored at the target aspect ratio renders undistorted; one authored
// at a different ratio (e.g. a square app icon reused for a non-square
// slot) would stretch, which is why the wizard banner/small-image
// sources are each authored at their own exact target aspect ratio
// rather than reusing the square app icon source.
func rasterizeToPNG(icon *oksvg.SvgIcon, width, height int) ([]byte, error) {
	icon.SetTarget(0, 0, float64(width), float64(height))
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// Explicit transparent fill: NewRGBA already zero-values to
	// transparent black, but be explicit rather than relying on the
	// zero value, since a non-transparent default would silently produce
	// a black-boxed icon that's easy to miss in a quick visual check.
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{0, 0, 0, 0})
		}
	}
	scanner := rasterx.NewScannerGV(width, height, img, img.Bounds())
	raster := rasterx.NewDasher(width, height, scanner)
	icon.Draw(raster, 1.0)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parseSize accepts either "N" (square NxN) or "WxH".
func parseSize(s string) (width, height int, err error) {
	if w, h, ok := strings.Cut(s, "x"); ok {
		width, err = strconv.Atoi(w)
		if err != nil {
			return 0, 0, err
		}
		height, err = strconv.Atoi(h)
		if err != nil {
			return 0, 0, err
		}
	} else {
		width, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, err
		}
		height = width
	}
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("width/height must be positive")
	}
	return width, height, nil
}

// encodeICO packs sizes/pngFrames into the ICO container format: a fixed
// ICONDIR header, one ICONDIRENTRY per frame, then the raw frame bytes
// back-to-back. bWidth/bHeight in each ICONDIRENTRY are single bytes,
// so a 256px frame must encode as 0, not 256 — an easy-to-miss off-by-one
// that silently produces a corrupt/oversized-looking entry if gotten
// wrong, so it's called out explicitly here rather than left implicit.
func encodeICO(sizes []int, pngFrames [][]byte) ([]byte, error) {
	if len(sizes) != len(pngFrames) {
		return nil, fmt.Errorf("sizes/pngFrames length mismatch")
	}

	var buf bytes.Buffer

	// ICONDIR: reserved(2)=0, type(2)=1 (icon), count(2)
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(len(sizes)))

	headerSize := 6 + 16*len(sizes)
	offset := headerSize
	for i, size := range sizes {
		dim := byte(size)
		if size >= 256 {
			dim = 0 // byte-256-means-0 rule
		}
		buf.WriteByte(dim)                                       // bWidth
		buf.WriteByte(dim)                                       // bHeight
		buf.WriteByte(0)                                         // bColorCount (0 = not palette-based)
		buf.WriteByte(0)                                         // bReserved
		binary.Write(&buf, binary.LittleEndian, uint16(1))       // wPlanes
		binary.Write(&buf, binary.LittleEndian, uint16(32))      // wBitCount (32bpp RGBA)
		binary.Write(&buf, binary.LittleEndian, uint32(len(pngFrames[i]))) // dwBytesInRes
		binary.Write(&buf, binary.LittleEndian, uint32(offset))  // dwImageOffset
		offset += len(pngFrames[i])
	}
	for _, frame := range pngFrames {
		buf.Write(frame)
	}
	return buf.Bytes(), nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "gen-icons: "+format+"\n", args...)
	os.Exit(1)
}
