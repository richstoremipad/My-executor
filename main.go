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
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// Warna terminal standar
var terminalColors = map[int]color.Color{
	30: color.RGBA{128, 128, 128, 255}, // Abu-abu
	31: color.RGBA{255, 85, 85, 255},   // Merah
	32: color.RGBA{85, 255, 85, 255},   // Hijau
	33: color.RGBA{255, 255, 85, 255},  // Kuning
	34: color.RGBA{85, 85, 255, 255},   // Biru
	35: color.RGBA{255, 85, 255, 255},  // Magenta
	36: color.RGBA{85, 255, 255, 255},  // Cyan (Logo XFILES)
	37: color.White,                    // Putih
	90: color.RGBA{170, 170, 170, 255}, // Abu Terang
	96: color.RGBA{128, 255, 255, 255}, // Cyan Terang
}

// Segment kustom untuk warna bebas
type terminalSegment struct {
	widget.TextSegment
	FgColor color.Color
}

func (s *terminalSegment) Visual() fyne.CanvasObject {
	t := canvas.NewText(s.Text, s.FgColor)
	t.TextStyle = fyne.TextStyle{Monospace: true}
	t.TextSize = 12
	return t
}

type terminalHandler struct {
	richText *widget.RichText
	scroll   *container.Scroll
	mutex    sync.Mutex
	lastFg   color.Color
}

func (h *terminalHandler) Write(p []byte) (n int, err error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	raw := string(p)
	runes := []rune(raw)
	var currentText strings.Builder

	for i := 0; i < len(runes); i++ {
		// Parse ANSI Escape Codes
		if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '[' {
			if currentText.Len() > 0 {
				h.appendSeg(currentText.String())
				currentText.Reset()
			}
			j := i + 2
			for j < len(runes) && !((runes[j] >= 'a' && runes[j] <= 'z') || (runes[j] >= 'A' && runes[j] <= 'Z')) {
				j++
			}
			if j < len(runes) {
				code := string(runes[i+2 : j])
				switch runes[j] {
				case 'm':
					parts := strings.Split(code, ";")
					for _, c := range parts {
						val, _ := strconv.Atoi(c)
						if val == 0 {
							h.lastFg = color.White
						} else if col, ok := terminalColors[val]; ok {
							h.lastFg = col
						}
					}
				case 'J', 'H': // Reset/Clear Screen
					h.richText.Segments = nil
				}
				i = j
				continue
			}
		}
		currentText.WriteRune(runes[i])
	}
	if currentText.Len() > 0 {
		h.appendSeg(currentText.String())
	}

	h.richText.Refresh()
	h.scroll.ScrollToBottom()
	return len(p), nil
}

func (h *terminalHandler) appendSeg(txt string) {
	txt = strings.ReplaceAll(txt, "\r", "")
	if txt == "" { return }
	seg := &terminalSegment{FgColor: h.lastFg}
	seg.Text = txt
	h.richText.Segments = append(h.richText.Segments, seg)
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("XFILES Loader")
	myWindow.Resize(fyne.NewSize(800, 600))

	outputRich := widget.NewRichText()
	outputRich.Wrapping = fyne.TextWrapOff // CEGAH LOGO PECAH

	logScroll := container.NewScroll(outputRich)
	logScroll.Direction = container.ScrollBoth // AKTIFKAN SCROLL SAMPING

	terminalBG := canvas.NewRectangle(color.Black) // BACKGROUND HITAM
	terminalArea := container.NewStack(terminalBG, logScroll)

	handler := &terminalBuffer{
		richText: outputRich,
		scroll:   logScroll,
		lastFg:   color.White,
	}

	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik perintah...")
	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		outputRich.Segments = nil
		outputRich.Refresh()
		statusLabel.SetText("Status: Berjalan...")

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)

		go func() {
			exec.Command("su", "-c", "rm "+targetPath).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 777 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			cmd := exec.Command("su", "-c", targetPath)
			if !bytes.HasPrefix(data, []byte("\x7fELF")) {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color",
				"COLUMNS=100",
				"LINES=30",
			)

			stdinPipe, _ = cmd.StdinPipe()
			cmd.Stdout = handler
			cmd.Stderr = handler
			_ = cmd.Run()
			statusLabel.SetText("Status: Selesai")
			stdinPipe = nil
		}()
	}

	btnSelect := widget.NewButton("Pilih File (Bash/SHC)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	btnSend := widget.NewButton("Kirim", func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			inputEntry.SetText("")
		}
	})

	top := container.NewVBox(btnSelect, statusLabel)
	bottom := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	myWindow.SetContent(container.NewBorder(top, bottom, nil, nil, terminalArea))
	myWindow.ShowAndRun()
}

// Handler buffer terminal kustom untuk stabilitas build
type terminalBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	mutex    sync.Mutex
	lastFg   color.Color
}

func (h *terminalBuffer) Write(p []byte) (n int, err error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	raw := string(p)
	runes := []rune(raw)
	var currentText strings.Builder
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\x1b' && i+1 < len(runes) && runes[i+1] == '[' {
			if currentText.Len() > 0 { h.appendSeg(currentText.String()); currentText.Reset() }
			j := i + 2
			for j < len(runes) && !((runes[j] >= 'a' && runes[j] <= 'z') || (runes[j] >= 'A' && runes[j] <= 'Z')) { j++ }
			if j < len(runes) {
				code := string(runes[i+2 : j])
				if runes[j] == 'm' {
					parts := strings.Split(code, ";")
					for _, c := range parts {
						val, _ := strconv.Atoi(c)
						if val == 0 { h.lastFg = color.White } else if col, ok := terminalColors[val]; ok { h.lastFg = col }
					}
				} else if runes[j] == 'J' || runes[j] == 'H' { h.richText.Segments = nil }
				i = j; continue
			}
		}
		currentText.WriteRune(runes[i])
	}
	if currentText.Len() > 0 { h.appendSeg(currentText.String()) }
	h.richText.Refresh(); h.scroll.ScrollToBottom()
	return len(p), nil
}

func (h *terminalBuffer) appendSeg(txt string) {
	txt = strings.ReplaceAll(txt, "\r", "")
	if txt == "" { return }
	seg := &terminalSegment{FgColor: h.lastFg}
	seg.Text = txt
	h.richText.Segments = append(h.richText.Segments, seg)
}

