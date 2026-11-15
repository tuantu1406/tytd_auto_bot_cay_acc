module tytd_go

go 1.26.0

require (
	github.com/go-telegram/bot v1.17.0
	github.com/gorilla/websocket v1.5.3
	google.golang.org/protobuf v1.36.9
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/driver/sqlite v1.6.0
	gorm.io/gorm v1.31.1
)

replace github.com/mattn/go-sqlite3 => github.com/mutecomm/go-sqlcipher/v4 v4.4.2

require (
	github.com/dlclark/regexp2/v2 v2.11.0 // indirect
	github.com/dop251/goja v0.0.0-20261105002551-2413f2132ac8 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	github.com/sashabaranov/go-openai v1.41.2 // indirect
	golang.org/x/image v0.21.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
