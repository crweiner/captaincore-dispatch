package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/acme/autocert"
)

var db *gorm.DB
var err error
var config = LoadConfiguration("config.json")

type httpHandlerFunc func(http.ResponseWriter, *http.Request)

type Config struct {
	Token   string `json:"token"`
	Host    string `json:"host"`
	Port    string `json:"port"`
	SSLMode string `json:"ssl_mode"`
}

type Task struct {
	gorm.Model
	Command string
	Status  string
}

func LoadConfiguration(file string) Config {
	var config Config
	configFile, err := os.Open(file)
	defer configFile.Close()
	if err != nil {
		fmt.Println(err.Error())
	}
	jsonParser := json.NewDecoder(configFile)
	jsonParser.Decode(&config)
	return config
}

func generateCertificateAuthority() {
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1653),
		Subject: pkix.Name{
			Organization:  []string{"CaptainCore"},
			Country:       []string{"USA"},
			Province:      []string{"PA"},
			Locality:      []string{"Lancaster"},
			StreetAddress: []string{"342 N Queen St"},
			PostalCode:    []string{"17603"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := &priv.PublicKey
	caB, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, priv)
	if err != nil {
		log.Println("create ca failed", err)
		return
	}

	// Public key
	certOut, err := os.Create("ca.crt")
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: caB})
	certOut.Close()
	log.Print("written cat.crt\n")

	// Private key
	keyOut, err := os.OpenFile("ca.key", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	keyOut.Close()
	log.Print("written ca.key\n")
}

func generateCert() {

	// Load CA
	catls, err := tls.LoadX509KeyPair("ca.crt", "ca.key")
	if err != nil {
		panic(err)
	}
	ca, err := x509.ParseCertificate(catls.Certificate[0])
	if err != nil {
		panic(err)
	}

	// Prepare certificate
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1658),
		Subject: pkix.Name{
			Organization:  []string{"CaptainCore"},
			Country:       []string{"USA"},
			Province:      []string{"PA"},
			Locality:      []string{"Lancaster"},
			StreetAddress: []string{"342 N Queen St"},
			PostalCode:    []string{"17603"},
			CommonName:    "CaptainCore",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		SubjectKeyId:          []byte{1, 2, 3, 4, 6},
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := &priv.PublicKey

	// Sign the certificate
	certB, err := x509.CreateCertificate(rand.Reader, cert, ca, pub, catls.PrivateKey)

	// Public key
	certOut, err := os.Create("cert.pem")
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certB})
	certOut.Close()
	log.Print("written cert.pem\n")

	// Private key
	keyOut, err := os.OpenFile("key.pem", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	keyOut.Close()
	log.Print("written key.pem\n")
}

func allTasks(w http.ResponseWriter, r *http.Request) {

	var tasks []Task
	db.Find(&tasks)

	json.NewEncoder(w).Encode(tasks)
}

func newTask(w http.ResponseWriter, r *http.Request) {
	fmt.Println("New Task Endpoint Hit")
	var task Task
	json.NewDecoder(r.Body).Decode(&task)

	task.Status = "Started"

	db.Create(&task)
	fmt.Fprintf(w, "New Task Successfully Created")

	// Starts running CaptainCore command
	go runCommand("captaincore "+task.Command, task)

}

func deleteTask(w http.ResponseWriter, r *http.Request) {

	vars := mux.Vars(r)
	command := vars["command"]

	var tasks Task
	db.Where("command = ?", command).Find(&tasks)
	db.Delete(&tasks)

	fmt.Fprintf(w, "Successfully Deleted Task")
}

func viewTask(w http.ResponseWriter, r *http.Request) {

	vars := mux.Vars(r)
	id := vars["id"]

	var tasks Task
	db.Where("id = ?", id).Find(&tasks)

	fmt.Println("{}", tasks)

	json.NewEncoder(w).Encode(tasks)
}

func updateTask(w http.ResponseWriter, r *http.Request) {

	vars := mux.Vars(r)
	command := vars["command"]

	var tasks Task
	db.Where("command = ?", command).Find(&tasks)

	tasks.Command = command

	db.Save(&tasks)
	fmt.Fprintf(w, "Successfully Updated Task")
}

func handleRequests() {

	var httpsSrv *http.Server
	var httpSrv *http.Server
	var m *autocert.Manager

	router := mux.NewRouter().StrictSlash(true)
	router.HandleFunc("/task/{id}", checkSecurity(viewTask)).Methods("GET")
	router.HandleFunc("/task/{id}", checkSecurity(updateTask)).Methods("PUT")
	router.HandleFunc("/task/{id}", checkSecurity(deleteTask)).Methods("DELETE")
	router.HandleFunc("/tasks", checkSecurity(newTask)).Methods("POST")
	router.HandleFunc("/tasks", checkSecurity(allTasks)).Methods("GET")

	if config.SSLMode == "development" {

		// Generate ca.crt and ca.key if not found
		caFile, err := os.Open("ca.crt")
		if err != nil {
			generateCertificateAuthority()
		}
		defer caFile.Close()

		// Generate cert.pem and key.pem for https://localhost
		generateCert()

		log.Fatal(http.ListenAndServeTLS(":"+config.Port, "cert.pem", "key.pem", handlers.LoggingHandler(os.Stdout, router)))
	}
	if config.SSLMode == "production" {

		// Manage Let's Encrypt SSL

		// Note: use a sensible value for data directory
		// this is where cached certificates are stored

		httpsSrv = &http.Server{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  120 * time.Second,
			Handler:      router,
		}

		//  handlers.LoggingHandler(os.Stdout, router

		dataDir := "."
		hostPolicy := func(ctx context.Context, host string) error {
			// Note: change to your real domain
			allowedHost := config.Host
			if host == allowedHost {
				return nil
			}
			return fmt.Errorf("acme/autocert: only %s host is allowed", allowedHost)
		}

		m = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: hostPolicy,
			Cache:      autocert.DirCache(dataDir),
		}

		httpsSrv.Addr = config.Host + ":443"
		httpsSrv.TLSConfig = &tls.Config{GetCertificate: m.GetCertificate}

		log.Fatal(httpsSrv.ListenAndServeTLS("", ""))

		// Spin up web server on port 80 to listen for autocert HTTP challenge

		httpSrv = &http.Server{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  120 * time.Second,
			Handler:      http.DefaultServeMux,
		}
		if m != nil {
			// allow autocert handle Let's Encrypt auth callbacks over HTTP.
			// https://github.com/golang/go/issues/21890
			httpSrv.Handler = m.HTTPHandler(httpSrv.Handler)
			httpSrv.Addr = ":80"
			err := httpSrv.ListenAndServe()
			if err != nil {
				log.Fatalf("httpSrv.ListenAndServe() failed with %s", err)
			}
		}

	}

}

func initialMigration() {

	// Migrate the schema
	db.AutoMigrate(&Task{})

}

func runCommand(cmd string, t Task) string {
	// Taken from: https://gist.github.com/danesparza/a651ac923d6313b9d1b7563c9245743b

	//	Split the entire command up using ' -' as the delimeter
	parts := strings.Split(cmd, " ")

	//	The first part is the command, the rest are the args:
	head := parts[0]
	arguments := parts[1:len(parts)]

	//	Format the command
	comand := exec.Command(head, arguments...)

	//	Sanity check -- capture stdout and stderr:
	var out bytes.Buffer
	var stderr bytes.Buffer
	comand.Stdout = &out
	comand.Stderr = &stderr

	//	Run the command
	comand.Run()

	fmt.Println(out.String())
	fmt.Println(stderr.String())

	t.Status = "Completed"
	db.Save(&t)

	return out.String()

}

func serverCmd() *cobra.Command {
	return &cobra.Command{
		Use: "server",
		RunE: func(cmd *cobra.Command, args []string) error {

			fmt.Println("Starting server https://" + config.Host + ":" + config.Port)

			// Handle Subsequent requests
			handleRequests()

			return nil
		},
	}
}

func checkSecurity(next httpHandlerFunc) httpHandlerFunc {
	return func(res http.ResponseWriter, req *http.Request) {
		header := req.Header.Get("token")
		if header != config.Token {
			res.WriteHeader(http.StatusUnauthorized)
			res.Write([]byte("401 - Unauthorized"))
			return
		}
		next(res, req)
	}
}

// main function to boot up everything
func main() {

	db, err = gorm.Open("sqlite3", "sql.db")
	if err != nil {
		panic("failed to connect database")
	}
	defer db.Close()

	initialMigration()

	cmd := &cobra.Command{
		Use:     "captaincore-dispatch",
		Short:   "CaptainCore Dispatch Server 💻",
		Version: "0.1",
	}

	cmd.SetUsageTemplate(`[33mUsage:[0m{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

[33mAvailable Commands:[0m {{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

[33mFlags:[0m 
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`)

	cmd.AddCommand(serverCmd())

	if err := cmd.Execute(); err != nil {
		//fmt.Println(err)
		os.Exit(0)
	}

}
