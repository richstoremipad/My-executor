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

// FIX: Menggunakan ThemeColorName agar tidak error build 'undefined'
var colorMap = map[string]fyne.ThemeColorName{
	"31": theme.ColorNameError,   // Merah
	"32": theme.ColorNameSuccess, // Hijau
	"33": theme.ColorNameWarning, // Kuning
	"34": theme.ColorNamePrimary, // Biru
	"36": theme.ColorNamePrimary, // Cyan
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	text := string(p)
	// Menangkap kode ANSI untuk warna
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	parts := re.Split(text, -1)
	codes := re.FindAllStringSubmatch(text, -1)

	currentColor := theme.ColorNameForeground

	for i, part := range parts {
		if part != "" {
			// Bypass bypass teks expires
			if strings.Contains(strings.ToLower(part), "expires:") {
				part = regexp.MustCompile(`(?i)Expires:.*`).ReplaceAllString(part, "Expires: 99 days")
			}

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
	// PENTING: Refresh di sini agar output langsung muncul (Anti Blank)
	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Fixed")
	myWindow.Resize(fyne.NewSize(600, 500))

	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapBreak
	logScroll := container.NewScroll(outputRich)

	statusLabel := widget.NewLabel("Status: Siap")
	inputEntry := widget.NewEntry()
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		statusLabel.SetText("Status: Booting Root...")
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/exec_now"
		data, _ := io.ReadAll(reader)
		reader.Close()

		go func() {
			// Pastikan file ditulis dengan benar ke folder tmp
			setup := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			setup.Stdin = bytes.NewReader(data)
			setup.Run()

			isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// Tambahkan ENV agar script tahu ini adalah terminal berwarna
			cmd.Env = append(os.Environ(), "TERM=xterm-256color")
			
			stdinPipe, _ = cmd.StdinPipe()
			buf := &customBuffer{richText: outputRich, scroll: logScroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			statusLabel.SetText("Status: Running...")
			err := cmd.Run()
			
			if err != nil {
				statusLabel.SetText("Status: Selesai (Error)")
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

