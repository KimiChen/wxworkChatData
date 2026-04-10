module wxworkChatData

go 1.21

require (
	github.com/NICEXAI/WeWorkFinanceSDK v0.0.0
	go.uber.org/zap v1.27.0
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/driver/mysql v1.5.7
	gorm.io/gorm v1.25.12
)

require (
	github.com/go-sql-driver/mysql v1.7.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)

replace github.com/NICEXAI/WeWorkFinanceSDK => ./WeWorkFinanceSDK
