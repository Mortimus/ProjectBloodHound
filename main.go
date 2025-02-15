package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/containers/common/libnetwork/types"
	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/bindings/network"
	"github.com/containers/podman/v5/pkg/specgen"
)

const (
	BLOODHOUND       = "docker.io/specterops/bloodhound:latest"
	NEO4J            = "docker.io/library/neo4j:4.4"
	POSTGRESQL       = "docker.io/library/postgres:16"
	NETWORK          = "BloodHound-CE-network"
	PSQLFOLDER       = "bloodhound-data/postgresql"
	NEO4JFOLDER      = "bloodhound-data/neo4j"
	ADMIN_NAME       = "admin"
	ADMIN_PASS       = "admin"
	BH_SUCC_START    = "Server started successfully"
	PSQL_SUCC_START  = "database system is ready to accept connections"
	NEO4J_SUCC_START = "Remote interface available"
)

func createFolders() error {
	err := os.MkdirAll(PSQLFOLDER, 0755)
	if err != nil {
		return err
	}
	err = os.MkdirAll(NEO4JFOLDER, 0755)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	pull := false
	showLogs := true
	createFolders()
	// make source full path
	// get the current working directory
	wd, err := os.Getwd()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	conn, err := bindings.NewConnection(context.Background(), "unix:///run/podman/podman.sock")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	networks, err := network.List(conn, nil)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
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
			os.Exit(1)
		}
	}
	psqlID, err := SpawnPostgresql(&conn, wd, pull, showLogs)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer stopContainers(&conn, psqlID)

	neo4jID, err := SpawnNeo4j(&conn, wd, pull, showLogs)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer stopContainers(&conn, neo4jID)

	fmt.Printf("Sleeping 15 seconds because PostgreSQL is slow...\n")
	time.Sleep(15 * time.Second)

	bloodhoundID, err := SpawnBloodhoundCE(&conn, wd, pull, showLogs)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer stopContainers(&conn, bloodhoundID)

	// <-bhReady

	// update postgresql password expiration
	// TODO
	fmt.Print("Press any key to exit.")
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

	s.OverlayVolumes = []*specgen.OverlayVolume{
		{
			Source:      path.Join(wd, PSQLFOLDER),  // local folder
			Destination: "/var/lib/postgresql/data", // container folder
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

	s.OverlayVolumes = []*specgen.OverlayVolume{
		{
			Source:      path.Join(wd, NEO4JFOLDER), // local folder
			Destination: "/data",                    // container folder
		},
	}
	remove := true
	s.Remove = &remove

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
	// publishExposedPorts := true
	// s.PublishExposedPorts = &publishExposedPorts

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
	fmt.Printf("Watching logs until %s is ready...\n", product)
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

		fmt.Printf("Done checking logs for now...\n")
		select {
		case <-ready:
			return nil
		default:
		}
		time.Sleep(5 * time.Second)
		fmt.Printf("Checking logs again...\n")
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
