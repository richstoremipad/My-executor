package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// customBuffer untuk menangani output berwarna secara real-time
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	window   fyne.Window
}

// Fungsi sederhana untuk mengubah beberapa kode ANSI menjadi warna Fyne
func parseAnsiToRich(input string) []fyne.RichTextSegment {
	// Menghapus karakter kontrol terminal yang tidak perlu
	reControl := regexp.MustCompile(`\x1b\[[0-9;]*[HJKJ]`)
	input = reControl.ReplaceAllString(input, "")

	// Logika penggantian Expires visual tetap dipertahankan
	reExp := regexp.MustCompile(`(?i)Expires:.*`)
	input = reExp.ReplaceAllString(input, "Expires: 99 days")

	// Parser ANSI sederhana untuk warna dasar
	segments := []fyne.RichTextSegment{}
	
	// Default warna putih tajam agar tidak buram
	currentStyle := fyne.TextStyle{Monospace: true}
	currentColor := color.White

	// Pecah teks berdasarkan kode warna
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	parts := re.Split(input, -1)
	codes := re.FindAllString(input, -1)

	for i, part := range parts {
		if part != "" {
			segments = append(segments, &widget.TextSegment{
				Text: part,
				Style: widget.RichTextStyle{
					TextStyle: currentStyle,
					ColorName: fyne.CurrentApp().Settings().Theme().Color(fyne.ThemeColorForeground, fyne.ThemeVariantDark),
				},
			})
		}
		if i < len(codes) {
			// Deteksi warna (contoh sederhana: 32=hijau, 36=cyan)
			code := codes[i]
			if code == "\x1b[0m" { currentColor = color.White }
		}
	}

	// Jika parser terlalu berat, gunakan segmen teks biasa yang tajam
	if len(segments) == 0 {
		segments = append(segments, &widget.TextSegment{Text: input, Style: widget.RichTextStyleInline})
	}
	
	return segments
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	newSegments := parseAnsiToRich(string(p))
	cb.richText.Segments = append(cb.richText.Segments, newSegments...)
	
	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Sharp Color")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Menggunakan RichText: Teks tajam, mendukung warna, dan TIDAK memicu keyboard
	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapBreak
	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan...")
		outputRich.Segments = nil // Reset log

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath).Run()
			statusLabel.SetText("Status: Berjalan...")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			// Kirimkan xterm-256color agar script memberikan kode warna
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin", "TERM=xterm-256color")

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{richText: outputRich, scroll: logScroll, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			cmd.Run()
			statusLabel.SetText("Status: Selesai")
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			inputEntry.SetText("") 
		}
	}

	btnSend := widget.NewButton("Kirim", sendInput)
	btnSelect := widget.NewButton("Pilih File", func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, e error) {
			if r != nil { executeFile(r) }
		}, myWindow)
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry),
		nil, nil, logScroll,
	))

	myWindow.ShowAndRun()
}

