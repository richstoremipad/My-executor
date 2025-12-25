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
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// --- CONFIG ---
// Ukuran font agar pas di layar
const TextFontSize = 11

// --- WARNA ANSI ---
var ansiColors = map[int]color.Color{
	30: color.RGBA{150, 150, 150, 255}, // Black/Gray
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

// --- CUSTOM RENDERER (Agar Background Color Jalan) ---
type coloredText struct {
	widget.TextSegment
	FgColor color.Color
	BgColor color.Color
}

// Fungsi ini dipanggil Fyne untuk menggambar teks
func (t *coloredText) Visual() fyne.CanvasObject {
	// 1. Objek Teks
	text := canvas.NewText(t.Text, t.FgColor)
	text.TextStyle = t.Style.TextStyle
	text.TextStyle.Monospace = true
	text.TextSize = TextFontSize

	// 2. Jika tidak ada background, kembalikan teks saja
	if t.BgColor == nil || t.BgColor == color.Transparent {
		return text
	}

	// 3. Jika ada background, tumpuk menggunakan Stack (Aman & Stabil)
	// Rectangle di bawah, Teks di atas
	rect := canvas.NewRectangle(t.BgColor)
	return container.NewStack(rect, text)
}

// --- TERMINAL LOGIC ---
type TerminalBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	mutex    sync.Mutex
	
	// State Warna
	currFg color.Color
	currBg color.Color
}

func NewTerminalBuffer(rt *widget.RichText, s *container.Scroll) *TerminalBuffer {
	return &TerminalBuffer{
		richText: rt,
		scroll:   s,
		currFg:   color.White,
		currBg:   color.Transparent,
	}
}

// Reset Layar
func (tb *TerminalBuffer) Clear() {
	tb.mutex.Lock()
	defer tb.mutex.Unlock()
	tb.richText.Segments = nil
	tb.currFg = color.White
	tb.currBg = color.Transparent
	tb.richText.Refresh()
}

// Fungsi Write Parse Manual (Tanpa Regex, Tanpa Error)
func (tb *TerminalBuffer) Write(p []byte) (n int, err error) {
	tb.mutex.Lock()
	defer tb.mutex.Unlock()

	input := string(p)
	// Ganti tab dengan 4 spasi manual agar rapi
	input = strings.ReplaceAll(input, "\t", "    ")
	
	runes := []rune(input)
	lenRunes := len(runes)

	// Buffer sementara untuk teks biasa sebelum ketemu kode warna
	var textBuf bytes.Buffer

	i := 0
	for i < lenRunes {
		char := runes[i]

		// Deteksi Escape ANSI (\x1b)
		if char == '\x1b' {
			// Flush text buffer yang ada sebelumnya
			if textBuf.Len() > 0 {
				tb.addSegment(textBuf.String())
				textBuf.Reset()
			}

			// Parsing Kode ANSI
			endIdx := i + 1
			foundEnd := false
			for endIdx < lenRunes {
				c := runes[endIdx]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					foundEnd = true
					endIdx++
					break
				}
				endIdx++
			}

			if foundEnd {
				ansiSeq := string(runes[i+1 : endIdx-1]) // Isi: "[31"
				cmd := runes[endIdx-1]                   // Cmd: 'm'
				tb.handleAnsi(ansiSeq, cmd)
				i = endIdx
				continue
			}
		}

		// Karakter biasa, masukkan ke buffer
		textBuf.WriteRune(char)
		i++
	}

	// Flush sisa text buffer
	if textBuf.Len() > 0 {
		tb.addSegment(textBuf.String())
	}

	tb.richText.Refresh()
	tb.scroll.ScrollToBottom()
	return len(p), nil
}

func (tb *TerminalBuffer) addSegment(text string) {
	// Hapus Carriage Return agar baris tidak numpuk
	text = strings.ReplaceAll(text, "\r", "")
	
	seg := &coloredText{
		TextSegment: widget.TextSegment{
			Text: text,
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{Monospace: true},
			},
		},
		FgColor: tb.currFg,
		BgColor: tb.currBg,
	}
	tb.richText.Segments = append(tb.richText.Segments, seg)
}

func (tb *TerminalBuffer) handleAnsi(paramRaw string, cmd rune) {
	// Bersihkan '[' jika ada
	if strings.HasPrefix(paramRaw, "[") {
		paramRaw = paramRaw[1:]
	}

	switch cmd {
	case 'm': // Ganti Warna
		parts := strings.Split(paramRaw, ";")
		for _, p := range parts {
			val, _ := strconv.Atoi(p)
			if val == 0 {
				tb.currFg = color.White
				tb.currBg = color.Transparent
			} else if val >= 30 && val <= 37 { // FG
				tb.currFg = ansiColors[val]
			} else if val >= 90 && val <= 97 { // Bright FG
				tb.currFg = ansiColors[val]
			} else if val >= 40 && val <= 47 { // BG
				tb.currBg = ansiColors[val-10]
			}
		}
	case 'J': // Clear Screen
		if strings.Contains(paramRaw, "2") {
			tb.richText.Segments = nil
		}
	}
}

// --- MAIN PROGRAM ---

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Executor Terminal")
	myWindow.Resize(fyne.NewSize(800, 600))

	// 1. SETUP OUTPUT (RICHTEXT)
	outputRich := widget.NewRichText()
	// PENTING: Matikan Wrapping agar ASCII art 'XFILES' lurus memanjang
	outputRich.Wrapping = fyne.TextWrapOff 
	outputRich.Scroll = container.ScrollNone

	// Scroll Container (Bisa geser kanan-kiri)
	logScroll := container.NewScroll(outputRich)
	logScroll.Direction = container.ScrollBoth

	// 2. BACKGROUND HITAM (Stack)
	bgRect := canvas.NewRectangle(color.RGBA{10, 10, 10, 255})
	terminalArea := container.NewStack(bgRect, logScroll)

	// 3. INPUT
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik perintah...")
	
	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	// 4. LOGIKA EKSEKUSI
	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		
		// Reset
		outputRich.Segments = nil
		outputRich.Refresh()
		statusLabel.SetText("Menyiapkan...")

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Hapus file lama & Copy file baru
			exec.Command("su", "-c", "rm "+targetPath).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 755 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()
			
			time.Sleep(500 * time.Millisecond) // Jeda sebentar
			statusLabel.SetText("Running...")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// ENV VARIABLES (PENTING)
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color",
				"COLUMNS=100", // Lebar terminal virtual
				"LINES=30",
			)

			stdinPipe, _ = cmd.StdinPipe()
			
			// Buat buffer handler
			termBuf := NewTerminalBuffer(outputRich, logScroll)
			cmd.Stdout = termBuf
			cmd.Stderr = termBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Selesai (Error)")
			} else {
				statusLabel.SetText("Selesai")
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
	btnSelect := widget.NewButton("Pilih File", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	// 5. LAYOUT
	topBar := container.NewVBox(btnSelect, statusLabel)
	bottomBar := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	content := container.NewBorder(topBar, bottomBar, nil, nil, terminalArea)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

