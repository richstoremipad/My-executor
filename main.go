package main

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// --- CONFIG ---
const TerminalFontSize = 10 // Ukuran font diperkecil agar muat banyak (mirip Termux)

// --- STRUKTUR DATA ---

type customBuffer struct {
	richText *widget.RichText
	scroll   *container.Scroll
	mutex    sync.Mutex 
	
	// State ANSI
	lastFgColor color.Color // Warna Teks
	lastBgColor color.Color // Warna Latar (PENTING untuk Logo Blok)
	lastBold    bool
}

// Map warna ANSI Standar
var ansiColors = map[int]color.Color{
	30: color.RGBA{180, 180, 180, 255}, // Black/Gray
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

// --- CUSTOM RENDERER (Agar support Background Color) ---

type coloredText struct {
	widget.TextSegment
	FgColor color.Color
	BgColor color.Color
}

func (t *coloredText) Visual() fyne.CanvasObject {
	// Objek Teks
	text := canvas.NewText(t.Text, t.FgColor)
	text.TextStyle = t.Style.TextStyle
	text.TextStyle.Monospace = true
	text.TextSize = TerminalFontSize

	// Jika tidak ada background, kembalikan teks saja (lebih ringan)
	if t.BgColor == nil || t.BgColor == color.Transparent {
		return text
	}

	// Jika ada background, buat kotak di belakang teks
	bg := canvas.NewRectangle(t.BgColor)
	
	// Gunakan Container Stack (Tumpuk: Bawah=Rect, Atas=Text)
	// Kita bungkus dalam container khusus agar ukurannya pas
	return container.New(layout.NewCustomPaddedLayout(0), bg, text)
}

func (t *coloredText) Update(o fyne.CanvasObject) {
	// Update logic (skip for static segments simplicity)
}

// Layout helper untuk background text agar pas
type customPaddedLayout struct {
	padding float32
}
func (l *customPaddedLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, child := range objects {
		child.Resize(size)
		child.Move(fyne.NewPos(0, 0))
	}
}
func (l *customPaddedLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) > 1 {
		return objects[1].MinSize() // Ukuran mengikuti Teks (objek ke-2)
	}
	return fyne.NewSize(0, 0)
}

// --- LOGIC PARSER ---

func (cb *customBuffer) appendAnsiText(text string) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	// 1. Ganti Tab dengan Spasi (Fyne kadang error render tab di RichText)
	text = strings.ReplaceAll(text, "\t", "    ")

	// Regex ANSI yang menangkap FG (30-37) dan BG (40-47)
	re := regexp.MustCompile(`\x1b\[([0-9;?]*)([a-zA-Z])`)

	parts := re.Split(text, -1)
	matches := re.FindAllStringSubmatch(text, -1)

	if len(parts) > 0 && parts[0] != "" {
		cb.addSegmentLocked(parts[0])
	}

	for i, match := range matches {
		paramStr := match[1]
		cmdChar := match[2]

		switch cmdChar {
		case "m": // --- WARNA ---
			codes := strings.Split(paramStr, ";")
			for _, c := range codes {
				val, _ := strconv.Atoi(c)
				if val == 0 {
					// Reset
					cb.lastFgColor = color.White
					cb.lastBgColor = nil // Reset background transparant
					cb.lastBold = false
				} else if val == 1 {
					cb.lastBold = true
				} else if val >= 30 && val <= 37 { // Foreground Standard
					cb.lastFgColor = ansiColors[val]
				} else if val >= 90 && val <= 97 { // Foreground Bright
					cb.lastFgColor = ansiColors[val]
				} else if val >= 40 && val <= 47 { // Background Standard
					// Mapping 40-47 ke warna 30-37 untuk diambil warnanya
					cb.lastBgColor = ansiColors[val-10] 
				}
			}

		case "J": // Clear Screen
			if strings.Contains(paramStr, "2") {
				cb.richText.Segments = nil
			}
		case "H", "l", "h": 
			// Abaikan cursor movement
		}

		if i+1 < len(parts) && parts[i+1] != "" {
			cb.addSegmentLocked(parts[i+1])
		}
	}

	cb.richText.Refresh()
	if len(cb.richText.Segments) > 0 {
		cb.scroll.ScrollToBottom()
	}
}

func (cb *customBuffer) addSegmentLocked(text string) {
	fg := cb.lastFgColor
	if fg == nil { fg = color.White }
	
	// Hapus Carriage Return agar baris rapi
	text = strings.ReplaceAll(text, "\r", "")

	seg := &coloredText{
		TextSegment: widget.TextSegment{
			Text: text,
			Style: widget.RichTextStyle{
				TextStyle: fyne.TextStyle{Monospace: true, Bold: cb.lastBold},
			},
		},
		FgColor: fg,
		BgColor: cb.lastBgColor,
	}
	cb.richText.Segments = append(cb.richText.Segments, seg)
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	cb.appendAnsiText(string(p))
	return len(p), nil
}

// --- MAIN ---

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Universal Terminal")
	myWindow.Resize(fyne.NewSize(800, 600)) // Lebar diperbesar untuk ASCII art

	// 1. OUTPUT AREA
	outputRich := widget.NewRichText()
	outputRich.Scroll = container.ScrollNone
	
	// PENTING: Matikan Wrapping agar ASCII art tidak turun ke bawah
	outputRich.Wrapping = fyne.TextWrapOff 
	
	// Gunakan ScrollBoth agar jika teks kepanjangan bisa digeser ke samping (seperti terminal asli)
	logScroll := container.NewScroll(outputRich)
	logScroll.Direction = container.ScrollBoth 

	// Background Hitam Pekat
	bgRect := canvas.NewRectangle(color.RGBA{10, 10, 10, 255})
	logArea := container.NewStack(bgRect, logScroll)

	// 2. INPUT AREA
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik perintah...")
	
	statusLabel := widget.NewLabel("Status: Idle")
	var stdinPipe io.WriteCloser

	// 3. LOGIC EXECUTE
	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Loading...")
		outputRich.Segments = nil
		outputRich.Refresh()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			exec.Command("su", "-c", "rm "+targetPath).Run()
			
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 755 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			statusLabel.SetText("Running...")

			var cmd *exec.Cmd
			// Gunakan 'setsid' agar script punya session terminal sendiri jika memungkinkan
			// Tapi untuk simpel kita gunakan sh/binary langsung
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// SETUP ENV AGAR TAMPILAN BAGUS
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color", // Support warna penuh
				"COLUMNS=120",         // Paksa lebar virtual terminal lebar
				"LINES=40",
			)

			stdinPipe, _ = cmd.StdinPipe()

			combinedBuf := &customBuffer{
				richText:    outputRich,
				scroll:      logScroll,
				lastFgColor: color.White,
			}

			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Exit (Code)")
			} else {
				statusLabel.SetText("Done")
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
	btnSelect := widget.NewButton("Load File", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	// Layout Akhir
	controls := container.NewVBox(btnSelect, statusLabel)
	bottomBar := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	
	content := container.NewBorder(controls, bottomBar, nil, nil, logArea)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

