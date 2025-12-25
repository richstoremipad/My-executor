package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Map warna ANSI ke warna Fyne
var ansiColors = map[int]color.Color{
	31: color.RGBA{255, 85, 85, 255},   // Red
	32: color.RGBA{85, 255, 85, 255},   // Green
	33: color.RGBA{255, 255, 85, 255},  // Yellow
	34: color.RGBA{85, 85, 255, 255},   // Blue
	35: color.RGBA{255, 85, 255, 255},  // Magenta
	36: color.RGBA{85, 255, 255, 255},  // Cyan (Logo XFILES)
	37: color.White,                    // White
	90: color.RGBA{128, 128, 128, 255}, // Gray
}

// Segment kustom untuk merender warna spesifik tanpa celah
type colorSegment struct {
	widget.TextSegment
	FgColor color.Color
}

func (s *colorSegment) Visual() fyne.CanvasObject {
	t := canvas.NewText(s.Text, s.FgColor)
	t.TextStyle = fyne.TextStyle{Monospace: true}
	t.TextSize = 12
	return t
}

type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	window   fyne.Window
	lastFg   color.Color
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	rawText := string(p)
	
	// Logika replace Expires tetap dipertahankan sesuai kode asli Anda
	if strings.Contains(strings.ToLower(rawText), "expires:") {
		lines := strings.Split(rawText, "\n")
		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), "expires:") {
				lines[i] = "Expires: 99 days"
			}
		}
		rawText = strings.Join(lines, "\n")
	}

	// Parser ANSI sederhana
	runes := []rune(rawText)
	var currentText strings.Builder

	for i := 0; i < len(runes); i++ {
		if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '[' {
			if currentText.Len() > 0 {
				cb.appendSegment(currentText.String())
				currentText.Reset()
			}
			j := i + 2
			for j < len(runes) && !((runes[j] >= 'a' && runes[j] <= 'z') || (runes[j] >= 'A' && runes[j] <= 'Z')) {
				j++
			}
			if j < len(runes) {
				code := string(runes[i+2 : j])
				if runes[j] == 'm' {
					parts := strings.Split(code, ";")
					for _, c := range parts {
						val, _ := strconv.Atoi(c)
						if val == 0 {
							cb.lastFg = color.White
						} else if col, ok := ansiColors[val]; ok {
							cb.lastFg = col
						}
					}
				}
				i = j
				continue
			}
		}
		currentText.WriteRune(runes[i])
	}
	if currentText.Len() > 0 {
		cb.appendSegment(currentText.String())
	}

	cb.richText.Refresh()
	cb.scroll.ScrollToBottom()
	return len(p), nil
}

func (cb *customBuffer) appendSegment(txt string) {
	txt = strings.ReplaceAll(txt, "\r", "")
	if txt == "" {
		return
	}
	seg := &colorSegment{FgColor: cb.lastFg}
	seg.Text = txt
	cb.richText.Segments = append(cb.richText.Segments, seg)
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Root Executor")
	myWindow.Resize(fyne.NewSize(800, 600))

	// Menggunakan RichText agar mendukung segmen warna
	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapOff // PENTING: Agar Logo XFILES tidak hancur/melipat

	logScroll := container.NewScroll(outputRich)
	logScroll.Direction = container.ScrollBoth // Dukungan scroll samping jika logo lebar

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini...")

	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputRich.Segments = nil // Bersihkan log
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			exec.Command("su", "-c", "rm "+targetPath).Run()
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
			
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp", "TERM=xterm-256color")

			stdinPipe, _ = cmd.StdinPipe()
			combinedBuf := &customBuffer{richText: outputRich, scroll: logScroll, window: myWindow, lastFg: color.White}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
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

	// Layout tetap sama dengan kode asli Anda
	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel),
		container.NewBorder(nil, nil, nil, btnSend, inputEntry), 
		nil, nil, container.NewStack(canvas.NewRectangle(color.Black), logScroll), // Background hitam agar warna terlihat
	))

	myWindow.ShowAndRun()
}

