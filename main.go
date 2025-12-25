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

// --- KONFIGURASI WARNA ANSI ---
// Map warna manual agar tidak ketergantungan library luar
var ansiColors = map[int]color.Color{
	30: color.RGBA{128, 128, 128, 255}, // Black/Gray
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

// --- TERMINAL SYSTEM ---

type VirtualTerminal struct {
	grid   *widget.TextGrid
	scroll *container.Scroll
	mutex  sync.Mutex

	// Cursor Position
	row int
	col int

	// Simpan warna sebagai variabel biasa (bukan struct TextGridStyle)
	// Ini menghindari error "invalid composite literal"
	currFg color.Color
	currBg color.Color
}

func NewVirtualTerminal() *VirtualTerminal {
	g := widget.NewTextGrid()
	g.ShowLineNumbers = false // Matikan nomor baris agar mirip terminal asli

	// Scroll container
	s := container.NewScroll(g)

	return &VirtualTerminal{
		grid:   g,
		scroll: s,
		row:    0,
		col:    0,
		currFg: color.White,       // Default text putih
		currBg: color.Transparent, // Default bg transparan
	}
}

// Fungsi Reset Layar (Clear Screen)
func (vt *VirtualTerminal) Clear() {
	vt.mutex.Lock()
	defer vt.mutex.Unlock()
	
	// Reset Grid
	vt.grid.SetText("")
	vt.row = 0
	vt.col = 0
	vt.currFg = color.White
	vt.currBg = color.Transparent
	vt.grid.Refresh()
}

// Core Logic: Menulis byte stream ke Grid
func (vt *VirtualTerminal) Write(p []byte) (n int, err error) {
	vt.mutex.Lock()
	defer vt.mutex.Unlock()

	input := string(p)
	runes := []rune(input)
	lenRunes := len(runes)

	i := 0
	for i < lenRunes {
		char := runes[i]

		// 1. Deteksi ANSI Escape Sequence (\x1b)
		if char == '\x1b' {
			// Cari akhir sequence (biasanya huruf m, J, H, K)
			endIdx := i + 1
			for endIdx < lenRunes {
				c := runes[endIdx]
				// ANSI command biasanya huruf a-z atau A-Z
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					endIdx++
					break
				}
				endIdx++
			}
			
			// Ambil kode ANSI (misal: "[31m")
			if endIdx <= lenRunes {
				ansiSeq := string(runes[i+1 : endIdx]) // skip \x1b
				vt.handleAnsi(ansiSeq)
				i = endIdx
				continue
			}
		}

		// 2. Deteksi Newline
		if char == '\n' {
			vt.row++
			vt.col = 0
			i++
			continue
		}

		// 3. Deteksi Carriage Return
		if char == '\r' {
			vt.col = 0
			i++
			continue
		}

		// 4. Deteksi Tab
		if char == '\t' {
			// Tab = 4 spasi
			for k := 0; k < 4; k++ {
				vt.putChar(' ')
			}
			i++
			continue
		}

		// 5. Karakter Biasa
		vt.putChar(char)
		i++
	}

	vt.grid.Refresh()
	vt.scroll.ScrollToBottom()
	return len(p), nil
}

// Helper untuk menaruh 1 karakter di grid
func (vt *VirtualTerminal) putChar(r rune) {
	// Pastikan Baris Cukup
	for len(vt.grid.Rows) <= vt.row {
		// Tambah baris kosong
		vt.grid.Rows = append(vt.grid.Rows, widget.TextGridRow{})
	}

	// Pastikan Kolom Cukup (isi spasi jika loncat)
	rowCells := vt.grid.Rows[vt.row].Cells
	for len(rowCells) <= vt.col {
		rowCells = append(rowCells, widget.TextGridCell{Rune: ' '})
	}

	// Buat Style baru saat ini juga (Local Scope)
	// Ini trik untuk menghindari error "Composite Literal" di struct global
	style := widget.TextGridStyle{
		FGColor: vt.currFg,
		BGColor: vt.currBg,
	}

	// Update Cell
	rowCells[vt.col] = widget.TextGridCell{
		Rune:  r,
		Style: style,
	}

	// Simpan kembali ke grid
	vt.grid.Rows[vt.row].Cells = rowCells
	vt.col++
}

// Parser ANSI Sederhana
func (vt *VirtualTerminal) handleAnsi(seq string) {
	if len(seq) < 2 { return }
	
	cmd := seq[len(seq)-1] // Huruf terakhir (m, J, dll)
	
	// Hapus '[' di awal dan huruf perintah di akhir untuk ambil angka
	paramRaw := seq
	if strings.HasPrefix(paramRaw, "[") {
		paramRaw = paramRaw[1 : len(paramRaw)-1]
	} else {
		return 
	}

	switch cmd {
	case 'm': // Ganti Warna
		parts := strings.Split(paramRaw, ";")
		for _, p := range parts {
			val, _ := strconv.Atoi(p)
			if val == 0 {
				vt.currFg = color.White
				vt.currBg = color.Transparent
			} else if val >= 30 && val <= 37 { // FG Color
				vt.currFg = ansiColors[val]
			} else if val >= 90 && val <= 97 { // Bright FG
				vt.currFg = ansiColors[val]
			} else if val >= 40 && val <= 47 { // BG Color
				// Map 40->30 untuk ambil warna dari map
				vt.currBg = ansiColors[val-10]
			}
		}
	case 'J': // Clear Screen
		if strings.Contains(paramRaw, "2") {
			// Clear logic
			vt.grid.SetText("")
			vt.row = 0
			vt.col = 0
		}
	}
}

// --- MAIN PROGRAM ---

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Terminal Executor")
	myWindow.Resize(fyne.NewSize(800, 600))

	// 1. Siapkan Terminal
	term := NewVirtualTerminal()

	// 2. Background Hitam Pekat (PENTING untuk TextGrid)
	// Kita gunakan Stack: Paling bawah Rectangle Hitam, Paling atas Terminal Scroll
	bgRect := canvas.NewRectangle(color.RGBA{10, 10, 10, 255})
	
	// Layout Terminal Area (Stack menumpuk objek)
	termContainer := container.NewStack(bgRect, term.scroll)

	// 3. Input & Controls
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik perintah...")
	
	statusLabel := widget.NewLabel("Status: Siap")
	var stdinPipe io.WriteCloser

	// 4. Eksekusi Script
	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Loading...")
		term.Clear()

		targetPath := "/data/local/tmp/temp_exec"
		data, _ := io.ReadAll(reader)
		isBinary := bytes.HasPrefix(data, []byte("\x7fELF"))

		go func() {
			// Setup File System
			exec.Command("su", "-c", "rm "+targetPath).Run()
			copyCmd := exec.Command("su", "-c", "cat > "+targetPath+" && chmod 755 "+targetPath)
			copyStdin, _ := copyCmd.StdinPipe()
			go func() {
				defer copyStdin.Close()
				copyStdin.Write(data)
			}()
			copyCmd.Run()

			// Beri jeda sedikit
			time.Sleep(500 * time.Millisecond)

			statusLabel.SetText("Running...")

			var cmd *exec.Cmd
			if isBinary {
				cmd = exec.Command("su", "-c", targetPath)
			} else {
				cmd = exec.Command("su", "-c", "sh "+targetPath)
			}

			// ENV VARIABLES (Penting untuk formatting ASCII)
			cmd.Env = append(os.Environ(),
				"PATH=/system/bin:/system/xbin:/vendor/bin:/data/local/tmp",
				"TERM=xterm-256color",
				"COLUMNS=80",
				"LINES=25",
			)

			stdinPipe, _ = cmd.StdinPipe()
			cmd.Stdout = term
			cmd.Stderr = term

			err := cmd.Run()
			if err != nil {
				statusLabel.SetText("Selesai (Error/Exit)")
			} else {
				statusLabel.SetText("Selesai")
			}
			stdinPipe = nil
		}()
	}

	// 5. Handling Input User
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

	// 6. Layout Akhir
	topBar := container.NewVBox(btnSelect, statusLabel)
	bottomBar := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	
	// Susun semua
	content := container.NewBorder(topBar, bottomBar, nil, nil, termContainer)

	myWindow.SetContent(content)
	myWindow.ShowAndRun()
}

