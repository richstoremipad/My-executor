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

// customBuffer untuk menangani output stream
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
}

// Peta warna ANSI ke Tema Fyne agar build sukses
var ansiThemeColors = map[string]fyne.ThemeColorName{
	"31": theme.ColorNameError,     // Merah
	"32": theme.ColorNameSuccess,   // Hijau
	"33": theme.ColorNameWarning,   // Kuning
	"34": theme.ColorNamePrimary,   // Biru
	"37": theme.ColorNameForeground,// Putih
	"91": theme.ColorNameError,
	"92": theme.ColorNameSuccess,
	"93": theme.ColorNameWarning,
}

func appendAnsiText(rt *widget.RichText, text string) {
	re := regexp.MustCompile(`\x1b\[([0-9;]+)m`)
	parts := re.Split(text, -1)
	codes := re.FindAllStringSubmatch(text, -1)

	currentThemeColor := theme.ColorNameForeground

	for i, part := range parts {
		if len(part) > 0 {
			// Manipulasi teks Expires
			if strings.Contains(strings.ToLower(part), "expires:") {
				reExp := regexp.MustCompile(`(?i)Expires:.*`)
				part = reExp.ReplaceAllString(part, "Expires: 99 days")
			}

			rt.Segments = append(rt.Segments, &widget.TextSegment{
				Text: part,
				Style: widget.RichTextStyle{
					ColorName: currentThemeColor,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
		}

		if i < len(codes) {
			codeStr := codes[i][1]
			subCodes := strings.Split(codeStr, ";")
			for _, c := range subCodes {
				if c == "0" {
					currentThemeColor = theme.ColorNameForeground
				} else if val, ok := ansiThemeColors[c]; ok {
					currentThemeColor = val
				}
			}
		}
	}
}

// Write menggunakan fyne.DoDelayed atau fyne.CurrentApp().Driver().CanvasForObject() 
// Namun cara paling universal adalah menggunakan fungsi pembantu di bawah ini:
func (cb *customBuffer) Write(p []byte) (n int, err error) {
	textCopy := string(p)
	
	// FIX: Menggunakan fungsi refresh yang tepat agar tidak blank
	cb.richText.Refresh() 
	
	// Tambahkan segmen teks
	appendAnsiText(cb.richText, textCopy)
	
	// Paksa update UI agar teks muncul seketika
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
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Berjalan...")
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Persiapan file
			exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath).Run()
			
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			// Mendukung warna di terminal
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin", "TERM=xterm-256color")

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{richText: outputRich, scroll: logScroll}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			
			// Callback saat selesai
			if err != nil {
				statusLabel.SetText("Status: Selesai dengan Error")
			} else {
				statusLabel.SetText("Status: Selesai")
			}
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

