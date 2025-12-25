package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Pro")
	myWindow.Resize(fyne.NewSize(600, 400))

	outputArea := widget.NewMultiLineEntry()
	statusLabel := widget.NewLabel("Status: Siap")

	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		
		statusLabel.SetText("Status: Menyiapkan Sandbox...")
		outputArea.SetText("Menyalin file ke internal storage...\n")

		// Path internal agar bisa chmod +x
		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "script_jalan.sh")

		// Copy file
		out, _ := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		io.Copy(out, reader)
		out.Close()

		// Menjalankan di Goroutine agar UI tetap responsif
		go func() {
			statusLabel.SetText("Status: Meminta Akses ROOT...")
			
			// Perintah su -c yang lebih kuat
			// Kita bungkus path dengan tanda kutip tunggal untuk menghindari error spasi
			cmdString := fmt.Sprintf("/system/bin/sh '%s'", internalPath)
			cmd := exec.Command("su", "-c", cmdString)

			output, err := cmd.CombinedOutput()

			// Update tampilan di UI Thread
			myWindow.Canvas().Refresh(outputArea)
			
			if err != nil {
				// Cek jika error karena izin root ditolak
				errMsg := err.Error()
				if strings.Contains(errMsg, "exit status 1") {
					errMsg = "Izin ROOT Ditolak atau Script Error"
				}
				outputArea.SetText(fmt.Sprintf("STATUS: ERROR\nLOG: %v\n\nOUTPUT:\n%s", errMsg, string(output)))
				statusLabel.SetText("Status: Gagal")
			} else {
				if len(output) == 0 {
					outputArea.SetText("Script berhasil dijalankan (Tanpa Output)")
				} else {
					outputArea.SetText(string(output))
				}
				statusLabel.SetText("Status: Sukses")
			}
		}()
	}

	btnSelect := widget.NewButton("Pilih File .sh", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if reader != nil {
				executeFile(reader)
			}
		}, myWindow)
		fd.Show()
	})

	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, statusLabel), nil, nil, nil, outputArea,
	))
	myWindow.ShowAndRun()
}
