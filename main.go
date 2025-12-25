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

// customBuffer menggunakan RichText
type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
}

// Peta warna dari ANSI Code ke Fyne Theme Color
// Menggunakan warna tema agar valid dan terlihat jelas di mode gelap/terang
var ansiThemeColors = map[string]fyne.ThemeColorName{
	"31": theme.ColorNameError,     // Merah
	"32": theme.ColorNameSuccess,   // Hijau
	"33": theme.ColorNameWarning,   // Kuning
	"34": theme.ColorNamePrimary,   // Biru
	"35": theme.ColorNameHover,     // Ungu (sebagai alternatif)
	"36": theme.ColorNamePrimary,   // Cyan
	"37": theme.ColorNameForeground,// Putih
	"91": theme.ColorNameError,     // Merah Terang
	"92": theme.ColorNameSuccess,   // Hijau Terang
	"93": theme.ColorNameWarning,   // Kuning Terang
}

// Fungsi Parser ANSI ke RichText
// Dijalankan di dalam UI Thread agar aman
func appendAnsiText(rt *widget.RichText, text string) {
	re := regexp.MustCompile(`\x1b\[([0-9;]+)m`)
	
	parts := re.Split(text, -1)
	codes := re.FindAllStringSubmatch(text, -1)

	currentThemeColor := theme.ColorNameForeground

	for i, part := range parts {
		if len(part) > 0 {
			// Manipulasi visual Expires
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

// Write yang AMAN UNTUK UI (Thread-Safe)
func (cb *customBuffer) Write(p []byte) (n int, err error) {
	// Salin data agar aman dari race condition
	textCopy := string(p)

	// PENTING: Bungkus update UI dengan RunOnUIThread [SOLUSI LAYAR BLANK]
	fyne.CurrentApp().Driver().RunOnUIThread(func() {
		appendAnsiText(cb.richText, textCopy)
		cb.richText.Refresh()
		cb.scroll.ScrollToBottom()
	})
	
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Ultimate")
	myWindow.Resize(fyne.NewSize(600, 500))

	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapBreak
	outputRich.Scroll = container.ScrollNone
	
	logScroll := container.NewScroll(outputRich)

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan...")
		
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)

		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath).Run()

			// Update status harus via UI Thread juga agar aman
			fyne.CurrentApp().Driver().RunOnUIThread(func() {
				statusLabel.SetText("Status: Berjalan...")
			})
			
			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}
			
			// TERM=xterm-256color wajib agar warna muncul
			cmd.Env = append(os.Environ(), 
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp", 
				"TERM=xterm-256color",
			)

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{richText: outputRich, scroll: logScroll}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			
			// Update final status via UI Thread
			fyne.CurrentApp().Driver().RunOnUIThread(func() {
				if err != nil {
					statusLabel.SetText("Status: Berhenti/Error")
					// Pesan error Merah
					outputRich.Segments = append(outputRich.Segments, &widget.TextSegment{
						Text: "\n[Error: " + err.Error() + "]",
						Style: widget.RichTextStyle{
							ColorName: theme.ColorNameError,
							TextStyle: fyne.TextStyle{Monospace: true},
						},
					})
				} else {
					statusLabel.SetText("Status: Selesai")
				}
				outputRich.Refresh()
				logScroll.ScrollToBottom()
			})
			stdinPipe = nil
		}()
	}

	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			
			// Input user warna Kuning (Warning)
			outputRich.Segments = append(outputRich.Segments, &widget.TextSegment{
				Text: "> " + inputEntry.Text + "\n",
				Style: widget.RichTextStyle{
					ColorName: theme.ColorNameWarning,
					TextStyle: fyne.TextStyle{Monospace: true},
				},
			})
			outputRich.Refresh()
			logScroll.ScrollToBottom()
			inputEntry.SetText("") 
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })
	
	btnSelect := widget.NewButton("Pilih File (Bash/SHC Binary)", func() {
		dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
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

