package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/a-h/templ"
)

var reAuthor = regexp.MustCompile(`__author__ = "[0-9]+, \\w+"`)

// Location in source code
type Location struct {
	Column int `json:"column"`
	Row    int `json:"row"`
}

type LinterResult struct {
	Code        string
	Location    Location
	EndLocation Location
	Message     string
	URL         string
}

// Result item returned by ruff's linter
type ruffLinterResult struct {
	//Cell        interface{} `json:"cell"`
	Code        string   `json:"code"`
	Filename    string   `json:"filename"`
	Location    Location `json:"location"`
	EndLocation Location `json:"end_location"`
	Message     string   `json:"message"`
	/*Fix         *struct {
		Applicability string `json:"applicability"`
		Edits         []struct {
			Content     string   `json:"content"`
			Location    Location `json:"location"`
			EndLocation Location `json:"end_location"`
		} `json:"edits"`
		Message string `json:"message"`
	} `json:"fix,omitempty"`*/
	//NoqaRow int    `json:"noqa_row"`
	URL string `json:"url"`
}

// Sanitizes filename for Content-Disposition HTTP header value.
func sanitizeFilename(s string) string {
	var res strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == '-' {
			res.WriteRune(r)
		} else {
			res.WriteRune('_')
		}
	}
	return res.String()
}

func processZip(out io.Writer, in []byte) (map[string][]LinterResult, error) {
	zipRd, err := zip.NewReader(bytes.NewReader(in), int64(len(in)))
	if err != nil {
		fmt.Println(err)
	}
	linterRes := make(map[string][]LinterResult)
	var fB bytes.Buffer
	var linterOut bytes.Buffer
	zipWr := zip.NewWriter(out)
	processFile := func(f *zip.File) error {
		fB.Reset()
		{
			rd, err := f.Open()
			if err != nil {
				return err
			}
			defer rd.Close()
			if _, err := io.Copy(&fB, rd); err != nil {
				return err
			}
		}

		wr, err := zipWr.CreateHeader(&f.FileHeader)
		if err != nil {
			return err
		}
		if f.Mode().IsRegular() && strings.HasSuffix(f.Name, ".py") {
			var fLintRes []LinterResult

			sc := bufio.NewScanner(bytes.NewReader(fB.Bytes()))
			hasAuthor := false
			for sc.Scan() {
				if reAuthor.MatchString(sc.Text()) {
					hasAuthor = true
				}
			}
			if err := sc.Err(); err != nil {
				return err
			}
			if !hasAuthor {
				fLintRes = append(fLintRes, LinterResult{
					Location:    Location{1, 1},
					EndLocation: Location{1, 1},
					Message:     "missing __author__ line",
				})
			}

			{
				linterOut.Reset()
				cmd := exec.Command(
					"python3", "-m", "ruff", "check",
					"--output-format=json",
					"-",
					"--select=F,E,W,N,I,D100,D101,D102,D103,D104,D105,D106,D107",
					"--fix",
				)
				cmd.Stdin = bytes.NewReader(fB.Bytes())
				cmd.Stdout = wr
				cmd.Stderr = &linterOut
				if err := cmd.Run(); err != nil {
					if ee, ok := err.(*exec.ExitError); ok &&
						ee.ExitCode() == 1 {
						// Linter found something
					} else {
						return err
					}
				}

				var ruffLintRes []ruffLinterResult
				if err := json.Unmarshal(linterOut.Bytes(), &ruffLintRes); err != nil {
					return err
				}
				for _, lr := range ruffLintRes {
					fLintRes = append(fLintRes, LinterResult{
						Code:        lr.Code,
						Location:    lr.Location,
						EndLocation: lr.EndLocation,
						Message:     lr.Message,
						URL:         lr.URL,
					})
				}
			}

			linterRes[f.Name] = fLintRes
		} else {
			if _, err := wr.Write(fB.Bytes()); err != nil {
				return err
			}
		}
		return nil
	}
	for _, f := range zipRd.File {
		if err := processFile(f); err != nil {
			return nil, err
		}
	}
	return linterRes, nil
}

func main() {
	title := "GU EPI Autocorrector"

	http.Handle("/", templ.Handler(TplHtmlDoc(TplUploadForm(), title)))
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))
	http.Handle("/upload", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") ||
			strings.ToUpper(r.Method) != "POST" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		file, fileHdr, err := r.FormFile("file")
		if err != nil {
			fmt.Println(err)
		}

		outFileName := strings.TrimSuffix(fileHdr.Filename, ".zip") + "_out.zip"

		bIn, err := io.ReadAll(file)
		if err != nil {
			fmt.Println(err)
		}

		var bOut bytes.Buffer
		lintRes, err := processZip(&bOut, bIn)
		if err != nil {
			fmt.Println(err)
		}

		if len(lintRes) == 0 {
			w.Header().Add("Content-Disposition", "attachment; filename="+sanitizeFilename(outFileName))
			w.Header().Add("Content-Length", strconv.Itoa(bOut.Len()))
			if _, err := w.Write(bOut.Bytes()); err != nil {
				fmt.Println(err)
			}
		} else {
			if err := TplHtmlDoc(TplLintResults(lintRes), title).Render(r.Context(), w); err != nil {
				fmt.Println(err)
			}
		}
	}))

	fmt.Println("Listening on :3000")
	http.ListenAndServe(":3000", nil)
}
