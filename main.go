package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/containers/common/libnetwork/types"
	"github.com/containers/podman/v5/pkg/api/handlers"
	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/bindings/network"
	"github.com/containers/podman/v5/pkg/specgen"

	spec "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	BLOODHOUND       = "docker.io/specterops/bloodhound:latest"
	NEO4J            = "docker.io/library/neo4j:4.4"
	POSTGRESQL       = "docker.io/library/postgres:16"
	NETWORK          = "BloodHound-CE-network"
	PSQLFOLDER       = "bloodhound-data/postgresql"
	NEO4JFOLDER      = "bloodhound-data/neo4j"
	BH_SUCC_START    = "Server started successfully"
	PSQL_SUCC_START  = "database system is ready to accept connections"
	NEO4J_SUCC_START = "Remote interface available"
)

var ADMIN_NAME string = "admin"
var ADMIN_PASS string = "admin"

func createFolders(base string) error {
	fmt.Printf("Ensuring data folders at %s\n", base)
	psqlPath := path.Join(base, PSQLFOLDER)
	err := os.MkdirAll(psqlPath, 0755)
	if err != nil {
		return err
	}
	neo4jPath := path.Join(base, NEO4JFOLDER)
	err = os.MkdirAll(neo4jPath, 0755)
	if err != nil {
		return err
	}
	return nil
}

func createConfig() error {
	// print path to home folder
	dirname, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	// fmt.Println(dirname)
	// check if folder exists and otherwise create it
	configPath := path.Join(dirname, ".pbh")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Printf("Creating config folder %s\n", configPath)
		os.Mkdir(configPath, 0755)
	}
	// Create SQLite database
	// check if file exists and otherwise create it
	dbPath := path.Join(configPath, "settings.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Printf("Creating database %s\n", dbPath)
		_, err := os.Create(dbPath)
		if err != nil {
			return err
		}
	}

	// create a config file
	return nil
}

func loadConfig() error {

	// load a config file
	return nil
}

func main() {
	// make source full path
	// get the current working directory
	wd, err := os.Getwd()
	if err != nil {
		fmt.Println(err)
		return
	}
	// currentFolderName := filepath.Base(wd)
	// pull cli flags
	pull := flag.Bool("pull", true, "Pull images before starting containers (docker has a rate limit per hour)")
	showLogs := flag.Bool("logs", false, "Show container logs")
	expiration := flag.String("expiration", time.Now().AddDate(10, 0, 0).Format("2006-01-02 15:04:05"), "Set password expiration date")
	customPath := flag.String("custom", "customqueries.json", "Path to custom queries json file in legacy bloodhound format")
	path := flag.String("path", wd, "Path to store data folders")
	forceInjection := flag.Bool("force", false, "Force injection of custom queries - will add duplicates if they already exist")
	// name := flag.String("name", currentFolderName, "Project Name for internal database usage")
	flag.Parse()
	// check to see if the path exists
	checkPath := filepath.Join(*path, PSQLFOLDER)
	if _, err := os.Stat(checkPath); os.IsNotExist(err) {
		fmt.Printf("New project detected, creating folders at %s\n", *path)
		forceInjection = boolPtr(true)
		err = nil
	}
	// check if the custom queries file exists
	if *forceInjection {
		if _, err := os.Stat(*customPath); os.IsNotExist(err) {
			fmt.Printf("Custom queries file not found at %s\n", *customPath)
			return
		}
	}

	err = createFolders(*path)
	if err != nil {
		fmt.Println(err)
		return
	}

	conn, err := bindings.NewConnection(context.Background(), "unix:///run/podman/podman.sock")
	if err != nil {
		fmt.Println(err)
		fmt.Printf("Is podman running as a service, and do you have permission (Administrator) to use it?\n")
		return
	}

	networks, err := network.List(conn, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Create a network if it doesn't exist
	var networkExists bool
	for _, n := range networks {
		if n.Name == NETWORK {
			networkExists = true
			break
		}
	}
	if !networkExists {
		_, err = network.Create(conn, &types.Network{
			Name:       NETWORK,
			DNSEnabled: *boolPtr(true),
		})
		if err != nil {
			fmt.Println(err)
			return
		}
	}
	psqlID, err := SpawnPostgresql(&conn, *path, *pull, *showLogs)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer stopContainers(&conn, psqlID)

	neo4jID, err := SpawnNeo4j(&conn, *path, *pull, *showLogs)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer stopContainers(&conn, neo4jID)

	bloodhoundID, err := SpawnBloodhoundCE(&conn, wd, *pull, *showLogs)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer stopContainers(&conn, bloodhoundID)
	fmt.Printf("Updating password expiration...\n")
	err = updatePasswordExpiration(&conn, psqlID, *expiration)
	if err != nil {
		fmt.Println(err)
		return
	}
	if *forceInjection {
		fmt.Printf("Injecting custom queries...\n")
		queries, err := readLegacyQueries(*customPath)
		if err != nil {
			fmt.Println(err)
			return
		}
		newQueries := legacyToNewQueries(queries)
		err = injectCustomQueries(&conn, psqlID, newQueries)
		if err != nil {
			fmt.Println(err)
			return
		}
	}

	fmt.Printf("Access bloodhound at http://127.0.0.1:8181\n")
	fmt.Printf("Username: %s\n", ADMIN_NAME)
	fmt.Printf("Password: %s\n", ADMIN_PASS)
	fmt.Print("Press any key to stop containers and exit...")
	input := bufio.NewScanner(os.Stdin)
	input.Scan()
	fmt.Printf("Cleaning up...\n")
}

func SpawnPostgresql(conn *context.Context, wd string, pull bool, showLogs bool) (string, error) {
	// postgresql image
	if pull {
		_, err := images.Pull(*conn, POSTGRESQL, nil)
		if err != nil {
			return "", err
		}
	}

	// Create a POSTGRES container
	s := specgen.NewSpecGenerator(POSTGRESQL, false)
	s.Name = "BloodHound-CE_PSQL"
	s.Env = map[string]string{
		"PGUSER":            "bloodhound",
		"POSTGRES_USER":     "bloodhound",
		"POSTGRES_PASSWORD": "bloodhoundcommunityedition",
		"POSTGRES_DB":       "bloodhound",
	}

	s.Networks = map[string]types.PerNetworkOptions{
		NETWORK: {
			Aliases: []string{"app-db"},
		},
	}

	s.Mounts = []spec.Mount{
		{
			Type:        "bind",
			Source:      path.Join(wd, PSQLFOLDER),
			Destination: "/var/lib/postgresql/data",
		},
	}

	remove := true
	s.Remove = &remove

	createResponse, err := containers.CreateWithSpec(*conn, s, nil)
	if err != nil {
		return "", err
	}
	fmt.Println("PostgreSQL Container created.")
	if err := containers.Start(*conn, createResponse.ID, nil); err != nil {
		return "", err
	}
	fmt.Println("PostgreSQL Container started.")
	// Wait until PostgreSQL is ready
	err = waitUntilReady(conn, createResponse.ID, PSQL_SUCC_START, "PostgreSQL", showLogs)
	if err != nil {
		return "", err
	}
	fmt.Printf("PostgreSQL is ready.\n")
	return createResponse.ID, nil
}

func SpawnNeo4j(conn *context.Context, wd string, pull bool, showLogs bool) (string, error) {
	// neo4j image
	if pull {
		_, err := images.Pull(*conn, NEO4J, nil)
		if err != nil {
			return "", err
		}
	}

	// Create a neo4j container
	s := specgen.NewSpecGenerator(NEO4J, false)
	s.Name = "BloodHound-CE_Neo4j"
	s.Env = map[string]string{
		"NEO4J_AUTH": "neo4j/bloodhoundcommunityedition",
	}

	s.Networks = map[string]types.PerNetworkOptions{
		NETWORK: {
			Aliases: []string{"graph-db"},
		},
	}

	s.Mounts = []spec.Mount{
		{
			Type:        "bind",
			Source:      path.Join(wd, NEO4JFOLDER),
			Destination: "/data",
		},
	}

	remove := true
	s.Remove = &remove

	s.PortMappings = []types.PortMapping{ // Expose ports for max and nxc
		{
			HostPort:      7474,
			ContainerPort: 7474,
		},
		{
			HostPort:      7687,
			ContainerPort: 7687,
		},
	}

	createResponse, err := containers.CreateWithSpec(*conn, s, nil)
	if err != nil {
		return "", err
	}
	fmt.Println("Neo4j Container created.")
	if err := containers.Start(*conn, createResponse.ID, nil); err != nil {
		return "", err
	}
	fmt.Println("Neo4j Container started.")
	// Wait until neo4j is ready
	err = waitUntilReady(conn, createResponse.ID, NEO4J_SUCC_START, "Neo4j", showLogs)
	if err != nil {
		return "", err
	}
	fmt.Printf("Neo4j is ready.\n")
	return createResponse.ID, nil
}

func SpawnBloodhoundCE(conn *context.Context, wd string, pull bool, showLogs bool) (string, error) {
	// bloodhound image
	if pull {
		_, err := images.Pull(*conn, BLOODHOUND, nil)
		if err != nil {
			return "", err
		}
	}

	// Create a bloodhound container
	s := specgen.NewSpecGenerator(BLOODHOUND, false)
	s.Name = "BloodHound-CE_BH"
	s.Env = map[string]string{
		// "bhe_disable_cypher_qc":            "false",
		"bhe_database_connection":          "user=bloodhound password=bloodhoundcommunityedition dbname=bloodhound host=app-db",
		"bhe_neo4j_connection":             "neo4j://neo4j:bloodhoundcommunityedition@graph-db:7687/",
		"bhe_default_admin_principal_name": ADMIN_NAME,
		"bhe_default_admin_password":       ADMIN_PASS,
	}

	s.PortMappings = []types.PortMapping{
		{
			HostPort:      8181,
			ContainerPort: 8080,
		},
	}

	s.Networks = map[string]types.PerNetworkOptions{
		NETWORK: {
			Aliases: []string{"bloodhound"},
		},
	}

	remove := true
	s.Remove = &remove

	createResponse, err := containers.CreateWithSpec(*conn, s, nil)
	if err != nil {
		return "", err
	}
	fmt.Println("Bloodhound Container created.")
	if err := containers.Start(*conn, createResponse.ID, nil); err != nil {
		return "", err
	}
	fmt.Println("Bloodhound Container started.")
	// Wait until bloodhound is ready
	err = waitUntilReady(conn, createResponse.ID, BH_SUCC_START, "Bloodhound", showLogs)
	if err != nil {
		return "", err
	}
	fmt.Printf("Bloodhound is ready.\n")
	return createResponse.ID, nil
}

func waitUntilReady(conn *context.Context, id string, success string, product string, showlogs bool) error {
	logs := make(chan string)
	ready := make(chan bool, 1)
	// loop reading logs until we are successful, if an error we restart the container
	if showlogs {
		fmt.Printf("Watching logs until %s is ready...\n", product)
	}
	go func() {
		for msg := range logs {
			if showlogs {
				fmt.Printf("%s: %s\n", product, msg)
			}
			if strings.Contains(msg, success) {
				ready <- true
			}
			if strings.Contains(msg, "ERROR") {
				fmt.Printf("%s failed to start.: %s\n", product, msg)
				ready <- false
			}
		}
	}()
	for {
		lo := &containers.LogOptions{
			Stdout: boolPtr(true),
			Stderr: boolPtr(true),
		}
		err := containers.Logs(*conn, id, lo, logs, logs)
		if err != nil {
			fmt.Println(err)
			return err
		}
		if showlogs {
			fmt.Printf("Done checking logs for now...\n")
		}
		select {
		case <-ready:
			return nil
		default:
		}
		time.Sleep(5 * time.Second)
		if showlogs {
			fmt.Printf("Checking logs again...\n")
		}
	}
}

func stopContainers(conn *context.Context, running ...string) error {
	for _, c := range running {
		// stop container
		if err := containers.Stop(*conn, c, nil); err != nil {
			return err
		} else {
			fmt.Printf("Container %s stopped.\n", c)
		}
	}
	return nil
}

func boolPtr(b bool) *bool {
	return &b
}

func updatePasswordExpiration(conn *context.Context, containerID string, expiration string) error {

	config := &handlers.ExecCreateConfig{}

	final := fmt.Sprintf("UPDATE auth_secrets SET expires_at='%s' WHERE id='1';", expiration)
	config.Cmd = []string{"psql", "-q", "-U", "bloodhound", "-d", "bloodhound", "-c", final}

	execID, err := containers.ExecCreate(*conn, containerID, config)
	if err != nil {
		return err
	}

	err = containers.ExecStart(*conn, execID, nil)
	if err != nil {
		return err
	}

	return nil
}

func insertCustomQuery(conn *context.Context, containerID string, name string, query string, description string) error {
	config := &handlers.ExecCreateConfig{}

	final := fmt.Sprintf("INSERT INTO saved_queries (user_id, name, query, description) SELECT (SELECT id FROM users WHERE principal_name = 'admin'), '%s', '%s', '%s' WHERE EXISTS (SELECT 1 FROM users WHERE principal_name = '%s');", name, query, description, ADMIN_NAME)

	config.Cmd = []string{"psql", "-q", "-U", "bloodhound", "-d", "bloodhound", "-c", final}

	execID, err := containers.ExecCreate(*conn, containerID, config)
	if err != nil {
		return err
	}

	err = containers.ExecStart(*conn, execID, nil)
	if err != nil {
		return err
	}

	return nil
}

type BloodHoundLegacyQuery struct {
	Name     string                `json:"name"`
	Category string                `json:"category"`
	Queries  []BloodHoundQueryItem `json:"queryList"`
}

type BloodHoundQueryItem struct {
	Final             bool              `json:"final"`
	Title             string            `json:"title,omitempty"` // Title is optional
	Query             string            `json:"query"`
	AllowCollapse     bool              `json:"allowCollapse,omitempty"`     // allowCollapse is optional
	Props             map[string]string `json:"props,omitempty"`             // props is optional
	RequireNodeSelect bool              `json:"requireNodeSelect,omitempty"` // requireNodeSelect is optional
	StartNode         string            `json:"startNode,omitempty"`         // startNode is optional
	EndNode           string            `json:"endNode,omitempty"`           // endNode is optional
}

type BloodHoundLegacyQueries struct {
	Queries []BloodHoundLegacyQuery `json:"queries"`
}

func readLegacyQueries(path string) (BloodHoundLegacyQueries, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BloodHoundLegacyQueries{}, err
	}
	var LegacyQueries BloodHoundLegacyQueries

	// 3. Unmarshal the JSON data into the struct
	err = json.Unmarshal(data, &LegacyQueries)
	if err != nil {
		return BloodHoundLegacyQueries{}, err
	}
	return LegacyQueries, nil
}

type BloodHoundQuery struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Query       string `json:"query"`
}

type BloodHoundQueries struct {
	Queries []BloodHoundQuery `json:"queries"`
}

func legacyToNewQueries(legacy BloodHoundLegacyQueries) BloodHoundQueries {
	var newQueries BloodHoundQueries
	for _, q := range legacy.Queries {
		query := LegacyQueryToSingleModernQuery(q)
		if query.Description != "Unimplemented" {
			newQueries.Queries = append(newQueries.Queries, query)
		}
	}
	return newQueries
}

func LegacyQueryToSingleModernQuery(legacy BloodHoundLegacyQuery) BloodHoundQuery {
	// if len(legacy.Queries) == 1 then return BloodHoundQuery
	if len(legacy.Queries) == 1 { // if there's a single query then return it
		name := fmt.Sprintf("[%s] %s", legacy.Category, legacy.Name)
		query := ""
		if legacy.Queries[0].Props != nil {
			query = inlinePropValue(legacy.Queries[0].Query, legacy.Queries[0].Props)
		} else {
			query = legacy.Queries[0].Query
		}
		return BloodHoundQuery{
			Name:        name,
			Description: legacy.Name,
			Query:       query,
		}
	}
	name := fmt.Sprintf("Multi Query not implemented yet: %s\n", legacy.Name)
	return BloodHoundQuery{
		Name:        name,
		Description: "Unimplemented",
		Query:       "//Unimplemented",
	}
	// // otherwise we need to combine the queries
	// name := fmt.Sprintf("[%s] %s", legacy.Category, legacy.Name)
	// desc := legacy.Name
	// query := ""
	// for _, q := range legacy.Queries {
	// 	query += q.Query + "\n"
	// }
	// return BloodHoundQuery{
	// 	Name:        name,
	// 	Description: desc,
	// 	Query:       query,
	// }
}

func inlinePropValue(query string, props map[string]string) string {
	// replace props in query
	for k, v := range props {
		// k is the variable name which is prepended by $ in the query
		// v is the value to replace it with
		variable := fmt.Sprintf("$%s", k)
		quoted := fmt.Sprintf("'%s'", v)
		query = strings.ReplaceAll(query, variable, quoted)
	}
	return query
}

func injectCustomQueries(conn *context.Context, containerID string, queries BloodHoundQueries) error {
	for i, q := range queries.Queries {
		fmt.Printf("Injecting query [%d/%d]: %s\n", i+1, len(queries.Queries), q.Name)
		err := insertCustomQuery(conn, containerID, q.Name, q.Query, q.Description)
		if err != nil {
			return err
		}
	}
	return nil
}
