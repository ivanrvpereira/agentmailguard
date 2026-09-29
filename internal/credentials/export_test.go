package credentials

// NewFileForTest keeps inexpensive scrypt work out of the production API.
func NewFileForTest(home, password string) *RecordStore { return newFile(home, password, 1) }
