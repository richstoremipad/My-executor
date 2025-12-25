package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
}

// Map warna ANSI ke Nama Warna Tema (Agar tidak error build)
var colorMap = map[string]fyne.ThemeColorName{
	"31": theme.ColorNameError,   // Merah
	"32": theme.ColorNameSuccess, // Hijau
	"33": theme.ColorNameWarning, // Kuning
	"34": theme.ColorNamePrimary, // Biru
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	text := string(p)
	
	// Regex untuk menangkap kode warna ANSI
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	parts := re.Split(text, -1)
	codes := re.FindAllStringSubmatch(text, -1)

	currentColor := theme.ColorNameForeground

	for i, part := range parts {
		if part != "" {
			// Ganti teks Expires secara otomatis
			if strings.Contains(strings.ToLower(part), "expires:") {
				part = regexp.MustCompile(`(?i)Expires:.*`).ReplaceAllString(part, "Expires: 99 days")
			}

			// Tambahkan teks dengan warna yang sesuai ke segmen
			cb.richText.Segments = append(cb.richText.Segments, &widget.TextSegment{
				Text: part,
				Style: widget.RichTextStyle{
					ColorName: currentColor,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
		}

		if i < len(codes) {
			code := codes[i][1]
			if val, ok := colorMap[code]; ok {
				currentColor = val
			} else if code == "0" || code == "" {
				currentColor = theme.ColorNameForeground
			}
		}
	}

	// Refresh UI agar teks muncul (Mencegah layar blank)
	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Color Fix")
	myWindow.Resize(fyne.NewSize(600, 500))

	// Menggunakan RichText agar bisa berwarna
	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapBreak
	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menjalankan...")
		outputRich.Segments = nil // Clear log
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Copy file ke folder sistem agar bisa dieksekusi
			exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath).Run()

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// TERM=xterm agar script mengeluarkan warna ANSI
			cmd.Env = append(os.Environ(), "TERM=xterm")
			stdinPipe, _ = cmd.StdinPipe()
			
			buf := &customBuffer{richText: outputRich, scroll: logScroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Error")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
			stdinPipe = nil
		}()
	}

	btnSelect := widget.NewButton("Pilih File", func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, e error) {
			if r != nil { executeFile(r) }
		}, myWindow)
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, statusLabel),
		container.NewBorder(nil, nil, nil, widget.NewButton("Kirim", func() {
			if stdinPipe != nil && inputEntry.Text != "" {
				fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
				inputEntry.SetText("")
			}
		}), inputEntry),
		nil, nil, logScroll,
	))

	myWindow.ShowAndRun()
}

