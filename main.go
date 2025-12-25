package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Root Executor Pro v3")
	myWindow.Resize(fyne.NewSize(600, 450))

	// Elemen Antarmuka
	outputArea := widget.NewMultiLineEntry()
	outputArea.SetPlaceHolder("Log output akan muncul di sini...")
	
	statusLabel := widget.NewLabel("Status: Siap")

	// Fungsi Eksekusi
	executeFile := func(reader fyne.URIReadCloser) {
		defer reader.Close()
		
		statusLabel.SetText("Status: Menyiapkan Sandbox...")
		outputArea.SetText("Sedang menyalin file ke folder internal...\n")

		// 1. Tentukan path aman di folder internal aplikasi
		// Android melarang eksekusi langsung dari /sdcard meskipun root
		internalPath := filepath.Join(myApp.Storage().RootURI().Path(), "exec_script.sh")

		// 2. Salin isi file
		out, err := os.OpenFile(internalPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			outputArea.SetText("Gagal membuat file internal: " + err.Error())
			return
		}
		_, err = io.Copy(out, reader)
		out.Close()

		if err != nil {
			outputArea.SetText("Gagal menyalin script: " + err.Error())
			return
		}

		// 3. Eksekusi di Goroutine (Mencegah Blank Hitam/Freeze)
		go func() {
			statusLabel.SetText("Status: Mengeksekusi (ROOT)...")

			// Memanggil Superuser (su) untuk menjalankan shell (sh)
			cmd := exec.Command("su", "-c", "sh "+internalPath)

			// INJEKSI PATH: Sangat penting agar perintah standar Linux (ls, echo, dll) ditemukan
			cmd.Env = append(os.Environ(), 
				"PATH=/product/bin:/apex/com.android.runtime/bin:/apex/com.android.art/bin:/system_ext/bin:/system/bin:/system/xbin:/odm/bin:/vendor/bin:/vendor/xbin",
				"HOME=/data/local/tmp",
			)

			// Gunakan Buffer untuk menangkap output secara aman
			var resOut bytes.Buffer
			cmd.Stdout = &resOut
			cmd.Stderr = &resOut

			// Menjalankan perintah
			err := cmd.Run()

			// Kembali ke UI Thread untuk update tampilan
			myWindow.Canvas().Refresh(outputArea)
			
			if err != nil {
				outputArea.SetText(fmt.Sprintf("STATUS: ERROR\nLog: %v\n\nOutput Terakhir:\n%s", err, resOut.String()))
				statusLabel.SetText("Status: Gagal")
			} else {
				if resOut.Len() == 0 {
					outputArea.SetText("Script berhasil dieksekusi tanpa output teks.")
				} else {
					outputArea.SetText(resOut.String())
				}
				statusLabel.SetText("Status: Berhasil")
			}
		}()
	}

	// Tombol-tombol
	btnSelect := widget.NewButton("Pilih & Jalankan Script", func() {
		fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, myWindow)
				return
			}
			if reader == nil {
				return
			}
			executeFile(reader)
		}, myWindow)
		fd.Show()
	})

	btnClear := widget.NewButton("Bersihkan Log", func() {
		outputArea.SetText("")
		statusLabel.SetText("Status: Siap")
	})

	// Layouting
	myWindow.SetContent(container.NewBorder(
		container.NewVBox(btnSelect, btnClear, statusLabel), 
		nil, nil, nil, 
		outputArea,
	))

	myWindow.ShowAndRun()
}

