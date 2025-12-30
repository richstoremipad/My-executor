package main

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/mobile"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

/* ==========================================
   CONFIG & ASSETS
========================================== */
const AppVersion = "1.4" // Versi Mouse Support
const ConfigURL = "https://raw.githubusercontent.com/tangsanrich/Fileku/main/executor.txt"
const CryptoKey = "RahasiaNegaraJanganSampaiBocorir"

// Ukuran Terminal Virtual (Sesuaikan agar pas di layar)
const TermRows = 30
const TermCols = 85

var currentDir string = "/sdcard"
var activeStdin *os.File // Pointer ke PTY Master
var cmdMutex sync.Mutex

//go:embed fd.png
var fdPng []byte

//go:embed driver.zip
var driverZip []byte

/* ==========================================
   CUSTOM WIDGET: INTERACTIVE GRID (MOUSE CLICK)
========================================== */
// Widget ini turunan dari TextGrid tapi bisa mendeteksi Tap/Klik
type InteractiveGrid struct {
	widget.TextGrid
	cellSize fyne.Size
	ptyInput *os.File
}

func NewInteractiveGrid() *InteractiveGrid {
	g := &InteractiveGrid{}
	g.ExtendBaseWidget(g)
	g.ShowLineNumbers = false
	return g
}

// Event saat layar disentuh (Tap)
func (g *InteractiveGrid) Tapped(e *fyne.PointEvent) {
	if activeStdin == nil { return }

	// Hitung perkiraan baris & kolom berdasarkan posisi tap
	// Kita perlu ukuran font rata-rata. Fyne agak tricky disini, 
	// jadi kita estimasi berdasarkan ukuran widget dibagi jumlah baris/kolom.
	
	size := g.Size()
	cellW := size.Width / float32(TermCols)
	cellH := size.Height / float32(TermRows)

	// Hitung Col & Row (1-based index untuk Xterm Mouse)
	col := int(e.Position.X/cellW) + 1
	row := int(e.Position.Y/cellH) + 1

	// Batasi agar tidak error
	if col < 1 { col = 1 }
	if col > TermCols { col = TermCols }
	if row < 1 { row = 1 }
	if row > TermRows { row = TermRows }

	// KIRIM KODE KLIK MOUSE X10 (Standard Terminal Click)
	// Format: ESC [ M <Button+32> <Col+32> <Row+32>
	// Button 0 (Left Click) + 32 = 32 (Space) -> ASCII
	btn := 32 
	x := col + 32
	y := row + 32
	
	mouseSeq := []byte{0x1b, '[', 'M', byte(btn), byte(x), byte(y)}
	
	cmdMutex.Lock()
	if activeStdin != nil {
		activeStdin.Write(mouseSeq)
	}
	cmdMutex.Unlock()
}

// Agar widget bisa discroll di mobile
func (g *InteractiveGrid) TappedSecondary(e *fyne.PointEvent) {}
func (g *InteractiveGrid) Dragged(e *fyne.DragEvent) {}
func (g *InteractiveGrid) DragEnd() {}

/* ==========================================
   TERMINAL LOGIC (RENDERER)
========================================== */
type Terminal struct {
	grid         *InteractiveGrid // Gunakan widget custom kita
	curRow       int
	curCol       int
	curStyle     *widget.CustomTextGridStyle
	mutex        sync.Mutex
	needsRefresh bool
	
	escBuffer    []byte
	inEsc        bool
}

func NewTerminal() *Terminal {
	g := NewInteractiveGrid()
	
	// Style Default: Teks Putih, Background Hitam (Sesuai Termux)
	defStyle := &widget.CustomTextGridStyle{
		FGColor: color.White,
		BGColor: color.Black,
	}

	term := &Terminal{
		grid:         g,
		curRow:       0,
		curCol:       0,
		curStyle:     defStyle,
		inEsc:        false,
		escBuffer:    make([]byte, 0, 100),
		needsRefresh: false,
	}

	// Inisialisasi Ukuran Awal
	term.resizeGrid(TermRows, TermCols)

	go func() {
		ticker := time.NewTicker(30 * time.Millisecond) // 30 FPS Update
		for range ticker.C {
			term.mutex.Lock()
			if term.needsRefresh {
				term.grid.Refresh()
				term.needsRefresh = false
			}
			term.mutex.Unlock()
		}
	}()
	return term
}

// Pastikan grid selalu punya ukuran tetap (Fixed Size Terminal)
// Ini kunci agar tampilan tidak berantakan
func (t *Terminal) resizeGrid(rows, cols int) {
	// Reset grid rows
	t.grid.Rows = make([]widget.TextGridRow, rows)
	for i := range t.grid.Rows {
		cells := make([]widget.TextGridCell, cols)
		for j := range cells {
			// Isi dengan spasi kosong & style default
			cells[j] = widget.TextGridCell{Rune: ' ', Style: t.curStyle}
		}
		t.grid.Rows[i] = widget.TextGridRow{Cells: cells}
	}
}

func (t *Terminal) Clear() {
	t.mutex.Lock()
	t.resizeGrid(TermRows, TermCols)
	t.curRow = 0
	t.curCol = 0
	t.needsRefresh = true
	t.mutex.Unlock()
}

func (t *Terminal) Write(p []byte) (int, error) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	i := 0
	for i < len(p) {
		b := p[i]
		
		// 1. ESC Sequence Start
		if !t.inEsc && b == 0x1b {
			t.inEsc = true
			t.escBuffer = t.escBuffer[:0]
			i++
			continue
		}

		// 2. Escape Parsing
		if t.inEsc {
			t.escBuffer = append(t.escBuffer, b)
			i++
			// Deteksi akhir sequence (huruf kapital/kecil)
			if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') {
				t.handleCSI(t.escBuffer)
				t.inEsc = false
			}
			if len(t.escBuffer) > 20 { t.inEsc = false } // Safety
			continue
		}

		// 3. Normal Character / Control Char
		switch b {
		case '\n': 
			t.curRow++
			if t.curRow >= TermRows { t.curRow = TermRows - 1 } // Jangan nambah baris, tapi stay
		case '\r': 
			t.curCol = 0
		case '\b': 
			if t.curCol > 0 { t.curCol-- }
		default:
			// Decode Rune (UTF-8 simple)
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError { 
				i++ 
				continue 
			}
			i += size - 1 // loop akan nambah 1, jadi kurangi 1 disini
			
			t.setChar(r)
			t.curCol++
		}
		i++
	}
	t.needsRefresh = true
	return len(p), nil
}

func (t *Terminal) handleCSI(seq []byte) {
	if len(seq) < 2 || seq[0] != '[' { return }
	
	cmd := seq[len(seq)-1]
	paramsStr := string(seq[1 : len(seq)-1])
	params := []int{}
	
	// Parse parameters "31;1" -> [31, 1]
	parts := strings.Split(paramsStr, ";")
	for _, p := range parts {
		var v int
		fmt.Sscanf(p, "%d", &v)
		params = append(params, v)
	}
	if len(params) == 0 { params = []int{0} }

	val := func(idx, def int) int {
		if idx < len(params) { 
			if params[idx] == 0 { return def }
			return params[idx] 
		}
		return def
	}

	switch cmd {
	case 'm': // Color & Style
		for _, code := range params {
			switch code {
			case 0: // Reset
				t.curStyle.FGColor = color.White
				t.curStyle.BGColor = color.Black
				t.curStyle.TextStyle.Bold = false
			case 1: t.curStyle.TextStyle.Bold = true
			// FG Colors
			case 30: t.curStyle.FGColor = color.RGBA{128,128,128,255}
			case 31: t.curStyle.FGColor = color.RGBA{255,100,100,255}
			case 32: t.curStyle.FGColor = color.RGBA{100,255,100,255} // Green
			case 33: t.curStyle.FGColor = color.RGBA{255,255,100,255} // Yellow
			case 34: t.curStyle.FGColor = color.RGBA{100,100,255,255}
			case 36: t.curStyle.FGColor = color.RGBA{0,255,255,255}
			case 37: t.curStyle.FGColor = color.White
			// BG Colors
			case 40: t.curStyle.BGColor = color.Black
			case 41: t.curStyle.BGColor = color.RGBA{200,50,50,255}
			case 42: t.curStyle.BGColor = color.RGBA{50,200,50,255}
			case 44: t.curStyle.BGColor = color.RGBA{50,50,200,255}
			case 47: t.curStyle.BGColor = color.White
			}
		}
	
	case 'H', 'f': // Cursor Position [row;colH
		r := val(0, 1) - 1
		c := val(1, 1) - 1
		t.curRow = r
		t.curCol = c
		// Bounds check
		if t.curRow < 0 { t.curRow = 0 }
		if t.curRow >= TermRows { t.curRow = TermRows - 1 }
		if t.curCol < 0 { t.curCol = 0 }
		if t.curCol >= TermCols { t.curCol = TermCols - 1 }

	case 'J': // Clear Screen
		mode := val(0, 0)
		if mode == 2 {
			t.resizeGrid(TermRows, TermCols) // Reset total
			t.curRow = 0
			t.curCol = 0
		}
	
	case 'K': // Clear Line (Penting untuk anti-ghosting)
		if t.curRow < len(t.grid.Rows) {
			rowCells := t.grid.Rows[t.curRow].Cells
			for x := t.curCol; x < len(rowCells); x++ {
				rowCells[x] = widget.TextGridCell{Rune: ' ', Style: t.curStyle}
			}
			t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: rowCells})
		}
	}
}

func (t *Terminal) setChar(r rune) {
	if t.curRow >= TermRows || t.curCol >= TermCols { return }
	
	rowCells := t.grid.Rows[t.curRow].Cells
	
	// Copy style untuk cell ini
	style := *t.curStyle
	style.TextStyle.Monospace = true // Paksa Monospace

	rowCells[t.curCol] = widget.TextGridCell{Rune: r, Style: &style}
	t.grid.SetRow(t.curRow, widget.TextGridRow{Cells: rowCells})
}

/* ===============================
              MAIN UI
================================ */
// Helper UI
func createBtn(label string, icon fyne.Resource, action func()) *widget.Button {
	b := widget.NewButtonWithIcon(label, icon, action)
	return b
}

func checkRoot() bool {
	return exec.Command("su", "-c", "id").Run() == nil
}

func main() {
	a := app.New()
	
	// SET TEMA GELAP
	a.Settings().SetTheme(theme.DarkTheme())

	w := a.NewWindow("ULTIMATE EXECUTOR")
	// Paksa Landscape di HP jika bisa, atau ukuran besar
	w.Resize(fyne.NewSize(800, 500)) 

	term := NewTerminal()
	statusLbl := widget.NewLabel("Status: Ready")
	statusLbl.Alignment = fyne.TextAlignCenter

	// --- TOMBOL KONTROL (NAVIGASI TUI) ---
	// Karena touch TUI susah, kita buat D-PAD Virtual
	sendKey := func(data []byte) {
		cmdMutex.Lock()
		if activeStdin != nil { activeStdin.Write(data) }
		cmdMutex.Unlock()
	}
	
	// Kode ANSI untuk Panah
	btnUp := widget.NewButtonWithIcon("", theme.MoveUpIcon(), func() { sendKey([]byte{0x1b, '[', 'A'}) })
	btnDown := widget.NewButtonWithIcon("", theme.MoveDownIcon(), func() { sendKey([]byte{0x1b, '[', 'B'}) })
	btnLeft := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() { sendKey([]byte{0x1b, '[', 'D'}) })
	btnRight := widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() { sendKey([]byte{0x1b, '[', 'C'}) })
	btnEnter := widget.NewButton("ENTER", func() { sendKey([]byte{'\n'}) })
	btnEnter.Importance = widget.HighImportance

	// Susunan Tombol Kontrol (Bawah Layar)
	dpad := container.NewHBox(
		layout.NewSpacer(),
		btnLeft, 
		container.NewVBox(btnUp, btnDown),
		btnRight,
		layout.NewSpacer(),
		btnEnter,
		layout.NewSpacer(),
	)

	// --- LOGIC EKSEKUSI ---
	executeTask := func(cmdText string, isScript bool, scriptPath string, isBinary bool) {
		statusLbl.SetText("Running...")
		
		go func() {
			var cmd *exec.Cmd
			
			// Setup Command (sama seperti sebelumnya)
			if isScript {
				if checkRoot() {
					target := "/data/local/tmp/temp_exec"
					exec.Command("su", "-c", "rm -f "+target).Run()
					exec.Command("su", "-c", fmt.Sprintf("cp %s %s && chmod 777 %s", scriptPath, target, target)).Run()
					if isBinary {
						cmd = exec.Command("su", "-c", target)
					} else {
						cmd = exec.Command("su", "-c", "sh "+target)
					}
				} else {
					cmd = exec.Command("sh", scriptPath)
				}
			} else {
				// Command biasa
				cmd = exec.Command("sh", "-c", cmdText)
			}

			// ENV WAJIB UNTUK TUI
			cmd.Env = append(os.Environ(), 
				"TERM=xterm-256color",
				"COLORTERM=truecolor",
			)

			// Start PTY
			ptmx, err := pty.Start(cmd)
			if err != nil {
				statusLbl.SetText("Error: " + err.Error())
				return
			}

			// SET UKURAN PTY AGAR SAMA DENGAN GRID KITA
			pty.Setsize(ptmx, &pty.Winsize{Rows: TermRows, Cols: TermCols, X: 0, Y: 0})

			cmdMutex.Lock()
			activeStdin = ptmx
			cmdMutex.Unlock()

			// Hubungkan Output PTY ke Terminal Widget kita
			io.Copy(term, ptmx)

			// Cleanup
			cmd.Wait()
			ptmx.Close()
			cmdMutex.Lock(); activeStdin = nil; cmdMutex.Unlock()
			statusLbl.SetText("Finished.")
		}()
	}

	// File Picker
	btnFile := widget.NewButtonWithIcon("OPEN FILE", theme.FileIcon(), func() {
		dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err == nil && r != nil {
				// Copy file ke temp
				data, _ := io.ReadAll(r)
				r.Close()
				tmpPath := filepath.Join(os.TempDir(), r.URI().Name())
				os.WriteFile(tmpPath, data, 0755)
				
				// Cek binary ELF
				isBin := bytes.HasPrefix(data, []byte("\x7fELF"))
				
				// Jalankan
				term.Clear()
				executeTask("", true, tmpPath, isBin)
			}
		}, w).Show()
	})

	btnClear := widget.NewButtonWithIcon("CLEAR", theme.ContentClearIcon(), func() {
		term.Clear()
	})

	// Layout Utama
	topBar := container.NewVBox(
		widget.NewLabelWithStyle("ULTIMATE EXECUTOR TUI", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		container.NewHBox(btnFile, layout.NewSpacer(), btnClear),
		statusLbl,
	)

	// Container Terminal (Hitam Pekat)
	termContainer := container.NewStack(
		canvas.NewRectangle(color.Black), // Background Hitam
		term.grid, // Grid Widget kita
	)

	// Struktur Layar
	content := container.NewBorder(
		topBar, // Atas
		dpad,   // Bawah (Tombol Kontrol)
		nil, nil,
		termContainer, // Tengah
	)

	w.SetContent(content)
	w.ShowAndRun()
}

