package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas" // Import ini sudah dipindah ke atas
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Struktur Buffer yang dimodifikasi untuk RichText
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	window   fyne.Window
	// Menyimpan state warna terakhir agar nyambung antar chunk output
	lastColor color.Color
	lastBold  bool
}

// Map warna ANSI standar ke warna Go
var ansiColors = map[int]color.Color{
	30: color.RGBA{100, 100, 100, 255}, // Black (Grayish for visibility)
	31: color.RGBA{255, 80, 80, 255},   // Red
	32: color.RGBA{80, 200, 80, 255},   // Green
	33: color.RGBA{255, 255, 80, 255},  // Yellow
	34: color.RGBA{100, 150, 255, 255}, // Blue
	35: color.RGBA{255, 100, 255, 255}, // Magenta
	36: color.RGBA{100, 255, 255, 255}, // Cyan
	37: color.White,                    // White
	90: color.RGBA{100, 100, 100, 255}, // Bright Black
	91: color.RGBA{255, 0, 0, 255},     // Bright Red
	92: color.RGBA{0, 255, 0, 255},     // Bright Green
	93: color.RGBA{255, 255, 0, 255},   // Bright Yellow
	94: color.RGBA{92, 92, 255, 255},   // Bright Blue
	95: color.RGBA{255, 0, 255, 255},   // Bright Magenta
	96: color.RGBA{0, 255, 255, 255},   // Bright Cyan
	97: color.White,                    // Bright White
}

// Struct Custom Segment untuk menangani warna spesifik
type coloredText struct {
	widget.TextSegment
	Color color.Color
}

func (t *coloredText) Update(o fyne.CanvasObject) {
	if text, ok := o.(*canvas.Text); ok {
		text.Text = t.Text
		text.Color = t.Color
		text.TextStyle = t.Style.TextStyle
		text.TextSize = 12 // Ukuran font monospace
		text.Refresh()
	}
}

func (t *coloredText) Visual() fyne.CanvasObject {
	text := canvas.NewText(t.Text, t.Color)
	text.TextStyle = t.Style.TextStyle
	text.TextStyle.Monospace = true
	text.TextSize = 12
	return text
}

// Fungsi utama parsing ANSI
func (cb *customBuffer) appendAnsiText(text string) {
	// Regex untuk memisahkan kode ANSI: \x1b[...m
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)

	// Split teks berdasarkan kode ANSI
	parts := re.Split(text, -1)
	matches := re.FindAllStringSubmatch(text, -1)

	// Proses bagian pertama (text sebelum kode warna pertama)
	if len(parts) > 0 && parts[0] != "" {
		cb.addColoredSegment(parts[0])
	}

	for i, match := range matches {
		codeStr := match[1]

		// Parsing kode (misal "1;31" -> Bold, Red)
		codes := strings.Split(codeStr, ";")
		for _, c := range codes {
			val, _ := strconv.Atoi(c)
			if val == 0 {
				// Reset
				cb.lastColor = color.White
				cb.lastBold = false
			} else if val == 1 {
				cb.lastBold = true
			} else if col, ok := ansiColors[val]; ok {
				cb.lastColor = col
			}
		}

		// Tambahkan teks setelah kode warna ini (jika ada)
		if i+1 < len(parts) && parts[i+1] != "" {
			cb.addColoredSegment(parts[i+1])
		}
	}

	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
}

func (cb *customBuffer) addColoredSegment(text string) {
	// Gunakan warna saat ini, atau fallback ke White
	currentColor := cb.lastColor
	if currentColor == nil {
		currentColor = color.White
	}

	// Buat segmen kustom
	seg := &coloredText{
		TextSegment: widget.TextSegment{
			Text: text,
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{Monospace: true, Bold: cb.lastBold},
			},
		},
		Color: currentColor,
	}

	cb.richText.Segments = append(cb.richText.Segments, seg)
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	rawText := string(p)

	// Logika replace user (Expires hack)
	re := regexp.MustCompile(`(?i)Expires:.*`)
	modifiedText := re.ReplaceAllString(rawText, "Expires: 99 days")

	// Panggil parser ANSI (harus di thread UI agar aman, atau panggil Refresh nanti)
	// Karena Write dipanggil dari Goroutine exec, kita tidak boleh update UI langsung.
	// Namun Fyne v2 seringkali handle refresh via Refresh().
	// Untuk keamanan thread, lebih baik gunakan window.Canvas().Refresh di dalam append jika crash,
	// tapi method .Refresh() pada widget biasanya thread-safe di versi baru.
	cb.appendAnsiText(modifiedText)

	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Root Executor (Color Support)")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Output menggunakan RichText untuk support warna
	outputRich := widget.NewRichText()
	outputRich.Scroll = container.ScrollNone

	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")

		// Reset log
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)

		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Langkah 1: Copy file
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Status: Berjalan...")

			// Langkah 2: Eksekusi
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// Inject TERM variable
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color")

			stdinPipe, _ = cmd.StdinPipe()

			combinedBuf := &customBuffer{
				richText:  outputRich,
				scroll:    logScroll,
				window:    myWindow,
				lastColor: color.White,
			}

			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
				// Manual append error merah
				combinedBuf.lastColor = ansiColors[31] // Merah
				combinedBuf.appendAnsiText("\n[Error: " + err.Error() + "]")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			
			// Tampilkan input user dengan warna Cyan
			tmpBuf := &customBuffer{richText: outputRich, scroll: logScroll, lastColor: ansiColors[36]}
			tmpBuf.appendAnsiText("> " + inputEntry.Text + "\n")

			inputEntry.SetText("")
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })
	btnSelect := widget.NewButton("Pilih File (Bash/SHC Binary)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				executeFile(reader)
			}
		}, myWindow)
		fd.Show()
	})
	btnClear := widget.NewButton("Bersihkan Log", func() {
		outputRich.Segments = nil
		outputRich.Refresh()
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry),
		nil, nil, logScroll,
	))

	myWindow.ShowAndRun()
}

