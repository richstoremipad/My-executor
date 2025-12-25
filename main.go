package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// customBuffer menggunakan RichText untuk mendukung warna
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	window   fyne.Window
}

// Peta warna sederhana dari kode ANSI ke warna Go
var ansiColors = map[string]color.Color{
	"30": color.Black,
	"31": color.RGBA{R: 255, G: 0, B: 0, A: 255},       // Merah
	"32": color.RGBA{R: 0, G: 255, B: 0, A: 255},       // Hijau
	"33": color.RGBA{R: 255, G: 255, B: 0, A: 255},     // Kuning
	"34": color.RGBA{R: 0, G: 0, B: 255, A: 255},       // Biru
	"35": color.RGBA{R: 255, G: 0, B: 255, A: 255},     // Magenta
	"36": color.RGBA{R: 0, G: 255, B: 255, A: 255},     // Cyan
	"37": color.White,
	"90": color.RGBA{R: 128, G: 128, B: 128, A: 255},   // Abu-abu
	"91": color.RGBA{R: 255, G: 100, B: 100, A: 255},   // Merah Terang
	"92": color.RGBA{R: 100, G: 255, B: 100, A: 255},   // Hijau Terang
	"93": color.RGBA{R: 255, G: 255, B: 100, A: 255},   // Kuning Terang
	"94": color.RGBA{R: 100, G: 100, B: 255, A: 255},   // Biru Terang
	"95": color.RGBA{R: 255, G: 100, B: 255, A: 255},   // Magenta Terang
	"96": color.RGBA{R: 100, G: 255, B: 255, A: 255},   // Cyan Terang
	"97": color.White,
}

// Fungsi untuk mengurai teks ANSI menjadi segmen RichText berwarna
func appendAnsiText(rt *widget.RichText, text string) {
	// Regex untuk memisahkan kode escape ANSI (contoh: \x1b[32m)
	re := regexp.MustCompile(`\x1b\[([0-9;]+)m`)
	
	// Memecah teks berdasarkan kode warna
	parts := re.Split(text, -1)
	codes := re.FindAllStringSubmatch(text, -1)

	// Warna default (Putih)
	currentColor := color.Color(color.White)

	for i, part := range parts {
		// 1. Tambahkan teks dengan warna saat ini (jika ada teksnya)
		if len(part) > 0 {
			// Fitur manipulasi Expires (sesuai request sebelumnya)
			if strings.Contains(strings.ToLower(part), "expires:") {
				reExp := regexp.MustCompile(`(?i)Expires:.*`)
				part = reExp.ReplaceAllString(part, "Expires: 99 days")
			}

			rt.Segments = append(rt.Segments, &widget.TextSegment{
				Text: part,
				Style: widget.RichTextStyle{
					ColorName: "", // Gunakan InlineColor
					InlineColor: currentColor,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
		}

		// 2. Jika ada kode warna berikutnya, ubah currentColor untuk iterasi selanjutnya
		if i < len(codes) {
			codeStr := codes[i][1] // Ambil angka dalam kurung, misal "32" atau "1;32"
			// Split jika ada titik koma (misal 1;32 untuk Bold Green -> kita ambil 32 aja yg simpel)
			subCodes := strings.Split(codeStr, ";")
			
			for _, c := range subCodes {
				if c == "0" {
					currentColor = color.White // Reset
				} else if val, ok := ansiColors[c]; ok {
					currentColor = val // Set warna baru
				}
			}
		}
	}
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	// Jangan hapus ANSI di sini, kita parse di fungsi appendAnsiText
	text := string(p)

	// Panggil fungsi parser warna
	appendAnsiText(cb.richText, text)

	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor (Color Support)")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Ganti Label dengan RichText untuk dukungan warna
	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapBreak
	outputRich.Scroll = container.ScrollNone // Kita handle scroll manual
	
	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		
		// Reset Log
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)

		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Status: Berjalan...")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			// PENTING: Ubah TERM ke xterm-256color agar script Bash mengeluarkan warna
			cmd.Env = append(os.Environ(), 
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp", 
				"TERM=xterm-256color",
			)

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{richText: outputRich, scroll: logScroll, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
				// Tampilkan pesan error dengan warna Merah
				outputRich.Segments = append(outputRich.Segments, &widget.TextSegment{
					Text: "\n[Error: " + err.Error() + "]",
					Style: widget.RichTextStyle{InlineColor: color.RGBA{255, 0, 0, 255}},
				})
				outputRich.Refresh()
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			// Tampilkan input user dengan warna Kuning agar beda
			outputRich.Segments = append(outputRich.Segments, &widget.TextSegment{
				Text: "> " + inputEntry.Text + "\n",
				Style: widget.RichTextStyle{InlineColor: color.RGBA{255, 255, 0, 255}},
			})
			outputRich.Refresh()
			logScroll.ScrollToBottom()
			inputEntry.SetText("") 
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })
	btnSelect := widget.NewButton("Pilih File (Bash/SHC Binary)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
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

