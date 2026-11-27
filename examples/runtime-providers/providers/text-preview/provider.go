// Package textpreview is text-preview in Go (D159, D184): a file processor
// with no dependency. It extracts the text of plain text and Markdown,
// previews them as an SVG of their first lines, and makes a thumbnail of a
// greyscale PGM image, the simplest image format there is, by averaging
// blocks of pixels. A processor for PDFs, photos and video implements the
// same two operations over a library or a service.
package textpreview

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

var textTypes = []string{"text/plain", "text/markdown"}

// PGM is the media type of a greyscale PGM image.
const PGM = "image/x-portable-graymap"

// Greymap is a greyscale image: its size and one byte per pixel, rows top
// to bottom.
type Greymap struct {
	Width, Height int
	Pixels        []byte
}

// ReadPGM reads a PGM image, P2 (ASCII) or P5 (binary), of up to 255
// levels.
func ReadPGM(data []byte) (Greymap, error) {
	at := 0
	space := func(b byte) bool { return unicode.IsSpace(rune(b)) }
	token := func() string {
		for {
			for at < len(data) && space(data[at]) {
				at++
			}
			if at >= len(data) || data[at] != '#' {
				break
			}
			for at < len(data) && data[at] != '\n' {
				at++
			}
		}
		start := at
		for at < len(data) && !space(data[at]) {
			at++
		}
		return string(data[start:at])
	}
	magic := token()
	if magic != "P2" && magic != "P5" {
		if magic == "" {
			magic = "no header"
		}
		return Greymap{}, fmt.Errorf("text-preview: %s is no PGM image", magic)
	}
	width, _ := strconv.Atoi(token())
	height, _ := strconv.Atoi(token())
	levels, _ := strconv.Atoi(token())
	if width <= 0 || height <= 0 || levels <= 0 || levels >= 256 {
		return Greymap{}, errors.New("text-preview: the PGM header is not a size and levels under 256")
	}
	pixels := make([]byte, width*height)
	if magic == "P5" {
		at++
		copy(pixels, data[min(at, len(data)):])
	} else {
		for i := range pixels {
			n, _ := strconv.Atoi(token())
			pixels[i] = byte(n)
		}
	}
	if levels != 255 {
		for i, p := range pixels {
			pixels[i] = byte((int(p)*255*2 + levels) / (2 * levels))
		}
	}
	return Greymap{Width: width, Height: height, Pixels: pixels}, nil
}

// WritePGM writes a greymap as a binary PGM, P5.
func WritePGM(image Greymap) []byte {
	header := fmt.Sprintf("P5\n%d %d\n255\n", image.Width, image.Height)
	return append([]byte(header), image.Pixels...)
}

// shrink is the image made no larger than maxWidth by maxHeight, each pixel
// the mean of the block it covers.
func shrink(image Greymap, maxWidth, maxHeight int) Greymap {
	ceil := func(a, b int) int { return (a + b - 1) / b }
	factor := max(1, ceil(image.Width, maxWidth), ceil(image.Height, maxHeight))
	width, height := ceil(image.Width, factor), ceil(image.Height, factor)
	pixels := make([]byte, width*height)
	for y := range height {
		for x := range width {
			sum, count := 0, 0
			for dy := 0; dy < factor && y*factor+dy < image.Height; dy++ {
				for dx := 0; dx < factor && x*factor+dx < image.Width; dx++ {
					sum += int(image.Pixels[(y*factor+dy)*image.Width+x*factor+dx])
					count++
				}
			}
			pixels[y*width+x] = byte((sum*2 + count) / (2 * count))
		}
	}
	return Greymap{Width: width, Height: height, Pixels: pixels}
}

func escapeXML(text string) string {
	var b strings.Builder
	for _, r := range text {
		if strings.ContainsRune(`<>&"'`, r) {
			fmt.Fprintf(&b, "&#%d;", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// textPreview is an SVG of the first lines of text.
func textPreview(text string, maxWidth, maxHeight int) *providers.Preview {
	const lineHeight = 18
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	lines = lines[:min(len(lines), max(1, (maxHeight-8)/lineHeight))]
	height := min(maxHeight, len(lines)*lineHeight+8)
	chars := max(4, maxWidth/8)
	var rows strings.Builder
	for i, line := range lines {
		if utf8.RuneCountInString(line) > chars {
			line = string([]rune(line)[:chars-1]) + "…"
		}
		fmt.Fprintf(&rows, `<text x="4" y="%d">%s</text>`, (i+1)*lineHeight, escapeXML(line))
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" font-family="monospace" font-size="13"><rect width="100%%" height="100%%" fill="#fff"/>%s</svg>`, maxWidth, height, rows.String())
	return &providers.Preview{MediaType: "image/svg+xml", Bytes: []byte(svg), Width: maxWidth, Height: height}
}

// Definition is the provider a Go server links.
func Definition() providers.Definition {
	return providers.MustDefine(providers.Definition{
		Name:       "text-preview",
		Implements: providers.Files,
		Config: map[string]providers.SettingSpec{
			"MAX_CHARS": {Optional: true, Description: "The longest text extract returns; 100000 when unset."},
		},
		Create: func(_ context.Context, settings providers.Settings, _ providers.Context) (any, error) {
			maxChars := 100_000
			if text, ok, err := settings.OptionalText("MAX_CHARS"); err != nil {
				return nil, err
			} else if ok {
				if maxChars, err = strconv.Atoi(text); err != nil {
					return nil, fmt.Errorf("text-preview: MAX_CHARS is %q, no number", text)
				}
			}
			return processor{maxChars: maxChars}, nil
		},
	})
}

type processor struct{ maxChars int }

func (processor) MediaTypes() []string { return append(append([]string{}, textTypes...), PGM) }

func (p processor) ExtractText(_ context.Context, file providers.FileInput) (*providers.ExtractedText, error) {
	if !providers.ReadsMediaType(textTypes, file.MediaType) {
		return nil, nil
	}
	text := string(file.Bytes)
	if runes := []rune(text); len(runes) > p.maxChars {
		return &providers.ExtractedText{Text: string(runes[:p.maxChars]), Truncated: true}, nil
	}
	return &providers.ExtractedText{Text: text}, nil
}

func (processor) Preview(_ context.Context, file providers.FileInput, options providers.PreviewOptions) (*providers.Preview, error) {
	maxWidth, maxHeight := options.MaxWidth, options.MaxHeight
	if maxWidth == 0 {
		maxWidth = 320
	}
	if maxHeight == 0 {
		maxHeight = 160
	}
	if providers.ReadsMediaType(textTypes, file.MediaType) {
		return textPreview(string(file.Bytes), maxWidth, maxHeight), nil
	}
	if providers.ReadsMediaType([]string{PGM}, file.MediaType) {
		image, err := ReadPGM(file.Bytes)
		if err != nil {
			return nil, err
		}
		thumb := shrink(image, maxWidth, maxHeight)
		return &providers.Preview{MediaType: PGM, Bytes: WritePGM(thumb), Width: thumb.Width, Height: thumb.Height}, nil
	}
	return nil, nil
}
