package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	clientPkg "github.com/influxdata/influxdb/client/v2"
)

const UNIX_MILLIS_TO_UNIX_NANOS = 1000 * 1000
const NUM_CHART_QUERIES_AT_ONCE = 3
const PROMETHEUS_TIMEOUT_MILLIS = 1000

type Config struct {
	pngPath           string
	influxdbHostname  string
	influxdbPort      string
	influxdbUsername  string
	influxdbPassword  string
	emailFrom         string
	emailTo           string
	emailSubject      string
	smtpHostPort      string
	doSendEmail       bool
	grafanaConfigPath string
}

type Point struct {
	Time  time.Time
	Value float64
}

func getConfigFromFlags() Config {
	config := Config{}
	flag.StringVar(&config.pngPath, "pngPath", "", "Path to save .png image to")
	flag.StringVar(&config.influxdbHostname, "influxdbHostname", "localhost", "Hostname for InfluxDB")
	flag.StringVar(&config.influxdbPort, "influxdbPort", "8086", "Port for InfluxDB")
	flag.StringVar(&config.influxdbUsername, "influxdbUsername", "admin", "Username for InfluxDB, e.g. admin")
	flag.StringVar(&config.influxdbPassword, "influxdbPassword", "", "Password for InfluxDB")
	flag.StringVar(&config.emailFrom, "emailFrom", "", "Email address to send report from; e.g. Reports <reports@monitoring.danstutzman.com>")
	flag.StringVar(&config.emailTo, "emailTo", "", "Email address to send report to")
	flag.StringVar(&config.emailSubject, "emailSubject", "", "Subject for email report")
	flag.StringVar(&config.smtpHostPort, "smtpHostPort", "",
		"Hostname and port for SMTP server; e.g. localhost:25")
	flag.StringVar(&config.grafanaConfigPath, "grafanaConfigPath", "",
		"Location of file produced by get_grafana_config.sh")
	flag.Parse()

	if config.pngPath == "" {
		log.Fatalf("You must specify -pngPath; try ./out.png")
	}
	if config.grafanaConfigPath == "" {
		log.Fatalf("You must specify -grafanaConfigPath")
	}
	if config.emailFrom == "" &&
		config.emailTo == "" &&
		config.emailSubject == "" &&
		config.smtpHostPort == "" {
		config.doSendEmail = false
	} else if config.emailFrom != "" &&
		config.emailTo != "" &&
		config.emailSubject != "" &&
		config.smtpHostPort != "" {
		config.doSendEmail = true
	} else {
		log.Fatalf("Please supply values for all of -emailFrom, -emailTo, -emailSubject, and -smtpHostPort or none of them")
	}

	return config
}

func main() {
	config := getConfigFromFlags()

	client, err := clientPkg.NewHTTPClient(clientPkg.HTTPConfig{
		Addr:     "http://" + config.influxdbHostname + ":" + config.influxdbPort,
		Username: config.influxdbUsername,
		Password: config.influxdbPassword,
	})
	if err != nil {
		log.Fatalf("Error from NewHTTPClient: %s", err)
	}

	dashboardsReader, err := os.Open(config.grafanaConfigPath)
	if err != nil {
		log.Fatalf("Error from Open: %s", err)
	}
	dashboards := parseDashboardsJson(dashboardsReader)

	multichart := NewMultiChart()
	for _, dashboard := range dashboards {
		multichart.WriteHeader(dashboard.Title)
		for _, row := range dashboard.Rows {
			for _, panel := range row.Panels {
				if panel.DataSource == "belugacdn" {
					continue
				}

				dbName, dataSourceFound := map[string]string{
					"":                   "mydb",
					"belugacdn_logs":     "mydb",
					"InfluxDB: cadvisor": "cadvisor",
				}[panel.DataSource]
				if !dataSourceFound {
					log.Fatalf("Panel has unknown datasource: %+v", panel)
				}

				for _, target := range panel.Targets {
					if target.DsType != "influxdb" {
						log.Fatalf("Expected dsType=influxdb in panel %+v", panel)
					}

					xMin := time.Now().AddDate(0, 0, -1).UTC()
					xMax := time.Now().UTC()
					yMin := ""
					if len(panel.YAxes) > 0 {
						yMin = panel.YAxes[0].Min
					}
					yMax := ""
					if len(panel.YAxes) > 0 {
						yMax = panel.YAxes[0].Max
					}

					var command string
					if target.Query != "" {
						command = target.Query
					} else {
						command = fmt.Sprintf(
							"SELECT mean(value) FROM %s WHERE $timeFilter GROUP BY time($__interval)",
							target.Measurement)
					}
					command = strings.Replace(command, "$timeFilter",
						fmt.Sprintf("time > %d", xMin.UnixNano()), 1)
					command = strings.Replace(command, "$__interval", "1h", 1)
					if command == "" {
						log.Fatalf("Blank query for panel %+v", panel)
					}
					points := query(client, dbName, command)

					if len(points) > 0 {
						image := drawChart(points, panel.Title, xMin, xMax, yMin, yMax)
						multichart.CopyChart(image)
					} else {
						multichart.WriteHeader("no points")
					}
				}
			}
		}
	}

	log.Printf("Writing %s", config.pngPath)
	multichart.SaveToPng(config.pngPath)

	if config.doSendEmail {
		sendMail(config.smtpHostPort, config.emailFrom,
			config.emailTo, config.emailSubject, "(see attached image)",
			config.pngPath)
	}
}
