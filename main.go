package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"fyne.io"
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

var colorMap = map[string]fyne.ThemeColorName{
	"31": theme.ColorNameError,   // Merah
	"32": theme.ColorNameSuccess, // Hijau
	"33": theme.ColorNameWarning, // Kuning
	"34": theme.ColorNamePrimary, // Biru
	"36": theme.ColorNamePrimary, // Cyan
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	text := string(p)
	re := regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	parts := re.Split(text, -1)
	codes := re.FindAllStringSubmatch(text, -1)

	currentColor := theme.ColorNameForeground

	for i, part := range parts {
		if part != "" {
			// Manipulasi teks Expires
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
	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Color Fix")
	myWindow.Resize(fyne.NewSize(600, 500))

	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapBreak
	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		statusLabel.SetText("Status: Menyiapkan File...")
		outputRich.Segments = nil
		outputRich.Refresh()

		// Gunakan path absolut yang valid di Android
		targetPath := "/data/local/tmp/exec_file"
		data, _ := io.ReadAll(reader)
		reader.Close()

		go func() {
			// Tulis file menggunakan shell root agar izin tepat
			// Menggunakan printf untuk menghindari masalah karakter khusus
			writeCmd := exec.Command("su", "-c", fmt.Sprintf("cat > %s && chmod 777 %s", targetPath, targetPath))
			writeCmd.Stdin = bytes.NewReader(data)
			if err := writeCmd.Run(); err != nil {
				statusLabel.SetText("Status: Gagal menulis file")
				return
			}

			statusLabel.SetText("Status: Berjalan...")
			
			// Deteksi apakah binary atau script
			isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			cmd.Env = append(os.Environ(), "TERM=xterm-256color", "PATH=/system/bin:/system/xbin:/data/local/tmp")
			stdinPipe, _ = cmd.StdinPipe()
			
			buf := &customBuffer{richText: outputRich, scroll: logScroll}
			cmd.Stdout = buf
			cmd.Stderr = buf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti (Error)")
				// Tampilkan detail error jika gagal jalan
				fmt.Fprintf(buf, "\n\x1b[31m[Error]: %v\x1b[0m\n", err)
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

