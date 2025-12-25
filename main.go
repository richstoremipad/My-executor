package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// customBuffer untuk menangkap output dan memperbarui UI secara real-time
type customBuffer struct {
	area   *widget.Entry
	window fyne.Window
}

// Fungsi untuk menghapus kode warna ANSI agar teks bersih
func cleanAnsi(str string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansi.ReplaceAllString(str, "")
}

func (cb *customBuffer) Write(p []byte) (n int, err error) {
	// 1. Bersihkan teks dari kode warna
	cleanText := cleanAnsi(string(p))

	// 2. Modifikasi teks "Expires" secara visual
	// Mencari teks "Expires:" dan mengganti sisa barisnya menjadi "99 days"
	re := regexp.MustCompile(`(?i)Expires:.*`)
	modifiedText := re.ReplaceAllString(cleanText, "Expires: 99 days")

	// 3. Update UI secara real-time
	cb.area.Append(modifiedText)
	cb.window.Canvas().Refresh(cb.area)
	return len(p), nil
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Interactive Root Executor (SHC)")
	myWindow.Resize(fyne.NewSize(600, 500))

	// UI Elements
	outputArea := widget.NewMultiLineEntry()
	outputArea.TextStyle = fyne.TextStyle{Monospace: true}
	
	inputEntry := widget.NewEntry()
	inputEntry.SetPlaceHolder("Ketik input di sini lalu tekan Kirim/Enter...")

	statusLabel := widget.NewLabel("Status: Siap")
	
	var stdinPipe io.WriteCloser

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		statusLabel.SetText("Status: Menyiapkan file...")
		outputArea.SetText("")

		// Simpan file ke folder internal aplikasi
		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "temp_bin")
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()

		// Berikan izin eksekusi penuh
		os.Chmod(internalPath, 0755)

		go func() {
			statusLabel.SetText("Status: Berjalan (Interaktif)...")
			
			// Eksekusi langsung (./) mendukung Bash dan SHC
			cmdString := fmt.Sprintf("cd %s && ./%s", filepath.Dir(internalPath), filepath.Base(internalPath))
			cmd := exec.Command("su", "-c", cmdString)
			
			// Setup Environment
			cmd.Env = append(os.Environ(), "PATH=/system/bin:/system/xbin:/vendor/bin", "TERM=dumb")

			// Hubungkan Stdin agar interaktif
			stdinPipe, _ = cmd.StdinPipe()
			
			combinedBuf := &customBuffer{area: outputArea, window: myWindow}
			cmd.Stdout = combinedBuf
			cmd.Stderr = combinedBuf

			err := cmd.Run()
			
			if err != nil {
				statusLabel.SetText("Status: Berhenti/Error")
				outputArea.Append("\n[Proses Keluar: " + err.Error() + "]\n")
			} else {
				statusLabel.SetText("Status: Selesai")
				outputArea.Append("\n[Proses Selesai]\n")
			}
			stdinPipe = nil
		}()
	}

	// Fungsi pengiriman input user ke proses root
	sendInput := func() {
		if stdinPipe != nil && inputEntry.Text != "" {
			fmt.Fprintf(stdinPipe, "%s\n", inputEntry.Text)
			outputArea.Append(fmt.Sprintf("> %s\n", inputEntry.Text)) 
			inputEntry.SetText("") 
		}
	}

	inputEntry.OnSubmitted = func(s string) { sendInput() }
	btnSend := widget.NewButton("Kirim", func() { sendInput() })

	btnSelect := widget.NewButton("Pilih File (Bash/SHC)", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil { executeFile(reader) }
		}, myWindow)
		fd.Show()
	})

	btnClear := widget.NewButton("Bersihkan Log", func() {
		outputArea.SetText("")
	})

	// Layouting
	inputContainer := container.NewBorder(nil, nil, nil, btnSend, inputEntry)
	controls := container.NewVBox(btnSelect, btnClear, statusLabel)

	myWindow.SetContent(container.NewBorder(
		controls,
		inputContainer, 
		nil, nil, 
		outputArea,
	))

	myWindow.ShowAndRun()
}

