package tytd_go

import (
	"archive/zip"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// GameAccount chứa thông tin một tài khoản game
type GameAccount struct {
	Username string
	Password string
	ServerID int
}

// LoadAccounts tự động nhận diện file .xlsx hoặc .csv
func LoadAccounts(filePath string) ([]GameAccount, error) {
	lower := strings.ToLower(filePath)
	if strings.HasSuffix(lower, ".xlsx") {
		return readXLSX(filePath)
	}
	// Mặc định đọc theo định dạng CSV
	return readCSV(filePath)
}

// ----------------------------------------------------------------------------
// Đọc file CSV
// ----------------------------------------------------------------------------
func readCSV(filePath string) ([]GameAccount, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("không thể mở file CSV: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // Số cột linh hoạt

	var accounts []GameAccount
	isHeader := true

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("lỗi đọc dòng CSV: %w", err)
		}
		if len(record) < 3 {
			continue
		}

		user := strings.TrimSpace(record[0])
		pass := strings.TrimSpace(record[1])
		srvStr := strings.TrimSpace(record[2])

		// Bỏ qua dòng header nếu có
		if isHeader {
			isHeader = false
			lowerUser := strings.ToLower(user)
			if lowerUser == "username" || lowerUser == "user" || lowerUser == "tài khoản" {
				continue
			}
		}

		if user == "" || pass == "" {
			continue
		}

		srvID, _ := strconv.Atoi(srvStr)
		if srvID <= 0 {
			srvID = 710001 // Server mặc định nếu không khai
		}

		accounts = append(accounts, GameAccount{
			Username: user,
			Password: pass,
			ServerID: srvID,
		})
	}

	return accounts, nil
}

// ----------------------------------------------------------------------------
// Đọc file Excel .xlsx thuần bằng archive/zip + encoding/xml (Zero dependency)
// ----------------------------------------------------------------------------
type sharedStringsXML struct {
	SI []struct {
		T string `xml:"t"`
	} `xml:"si"`
}

type worksheetXML struct {
	SheetData struct {
		Row []struct {
			C []struct {
				R string `xml:"r,attr"`
				T string `xml:"t,attr"`
				V string `xml:"v"`
			} `xml:"c"`
		} `xml:"row"`
	} `xml:"sheetData"`
}

func readXLSX(filePath string) ([]GameAccount, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, fmt.Errorf("không thể mở file .xlsx: %w", err)
	}
	defer zr.Close()

	var sharedStrings []string
	var sheetFile *zip.File

	for _, f := range zr.File {
		if f.Name == "xl/sharedStrings.xml" {
			rc, err := f.Open()
			if err == nil {
				var ss sharedStringsXML
				if xml.NewDecoder(rc).Decode(&ss) == nil {
					for _, si := range ss.SI {
						sharedStrings = append(sharedStrings, si.T)
					}
				}
				rc.Close()
			}
		} else if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			if sheetFile == nil {
				sheetFile = f
			}
		}
	}

	if sheetFile == nil {
		return nil, fmt.Errorf("không tìm thấy trang tính (worksheet) trong file .xlsx")
	}

	rc, err := sheetFile.Open()
	if err != nil {
		return nil, fmt.Errorf("lỗi mở worksheet: %w", err)
	}
	defer rc.Close()

	var ws worksheetXML
	if err := xml.NewDecoder(rc).Decode(&ws); err != nil {
		return nil, fmt.Errorf("lỗi phân tích xml worksheet: %w", err)
	}

	var accounts []GameAccount
	isHeader := true

	for _, row := range ws.SheetData.Row {
		var rowVals []string
		for _, c := range cValues(row.C, sharedStrings) {
			rowVals = append(rowVals, c)
		}

		if len(rowVals) < 3 {
			continue
		}

		user := strings.TrimSpace(rowVals[0])
		pass := strings.TrimSpace(rowVals[1])
		srvStr := strings.TrimSpace(rowVals[2])

		if isHeader {
			isHeader = false
			lowerUser := strings.ToLower(user)
			if lowerUser == "username" || lowerUser == "user" || lowerUser == "tài khoản" {
				continue
			}
		}

		if user == "" || pass == "" {
			continue
		}

		srvID, _ := strconv.Atoi(srvStr)
		if srvID <= 0 {
			srvID = 710001
		}

		accounts = append(accounts, GameAccount{
			Username: user,
			Password: pass,
			ServerID: srvID,
		})
	}

	return accounts, nil
}

// Lấy giá trị ô cell từ xml
func cValues(cells []struct {
	R string `xml:"r,attr"`
	T string `xml:"t,attr"`
	V string `xml:"v"`
}, sharedStrings []string) []string {
	var res []string
	for _, c := range cells {
		val := c.V
		if c.T == "s" { // Chuỗi nằm trong sharedStrings
			idx, err := strconv.Atoi(val)
			if err == nil && idx >= 0 && idx < len(sharedStrings) {
				val = sharedStrings[idx]
			}
		}
		res = append(res, val)
	}
	return res
}

// Tạo file CSV mẫu nếu người dùng chưa có file
func CreateSampleCSV(filePath string) error {
	content := "username,password,server_id\n" +
		"taikhoan01,matkhau123,710010\n" +
		"taikhoan02,matkhau456,710015\n"
	return os.WriteFile(filePath, []byte(content), 0644)
}
