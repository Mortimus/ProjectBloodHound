package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/containers/common/libnetwork/types"
	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/bindings/network"
	"github.com/containers/podman/v5/pkg/specgen"
)

const (
	BLOODHOUND  = "docker.io/specterops/bloodhound:latest"
	NEO4J       = "docker.io/library/neo4j:4.4"
	POSTGRESQL  = "docker.io/library/postgres:16"
	NETWORK     = "BloodHound-CE-network"
	PSQLFOLDER  = "bloodhound-data/postgresql"
	NEO4JFOLDER = "bloodhound-data/neo4j"
	ADMIN_NAME  = "admin"
	ADMIN_PASS  = "admin"
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
	psqlID, err := SpawnPostgresql(&conn, wd)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer stopContainers(&conn, psqlID)
	neo4jID, err := SpawnNeo4j(&conn, wd)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer stopContainers(&conn, neo4jID)
	// Wait for postgresql and neo4j to be ready before running bloodhound
	dbOut := make(chan string)
	defer close(dbOut)
	dbErr := make(chan string)
	defer close(dbErr)
	neo4jReady := make(chan struct{})
	defer close(neo4jReady)
	lo := &containers.LogOptions{
		Follow: boolPtr(true),
		Stdout: boolPtr(true),
		Stderr: boolPtr(true),
	}

	go func() {
		for msg := range dbOut {
			fmt.Printf("Neo4j: %s\n", msg)
			if strings.Contains(msg, "Remote interface available at http://localhost:7474/") {
				fmt.Println("Neo4j is ready.")
				neo4jReady <- struct{}{}
				break
			}
		}
	}()
	fmt.Printf("Capturing Logs...\n")
	// go func() {
	// 	for err := range dbErr {
	// 		fmt.Printf("Neo4j[ERROR]: %s\n", err)
	// 	}
	// }()
	go func() { // Why the fuck does this need to be in a goroutine
		err = containers.Logs(conn, neo4jID, lo, dbOut, dbErr)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}()
	fmt.Printf("Waiting for Neo4j to be ready...\n")
	<-neo4jReady

	bloodhoundID, err := SpawnBloodhoundCE(&conn, wd)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer stopContainers(&conn, bloodhoundID)

	// Wait until bloodhound is ready
	bhOut := make(chan string)
	defer close(bhOut)
	bhErr := make(chan string)
	defer close(bhErr)
	bhReady := make(chan struct{})
	defer close(bhReady)
	go func() {
		for msg := range dbOut {
			fmt.Printf("Bloodhound: %s\n", msg)
			if strings.Contains(msg, "Server started successfully") {
				fmt.Println("Bloodhound is ready.")
				bhReady <- struct{}{}
				break
			}
		}
	}()
	go func() {
		for msg := range bhErr {
			fmt.Printf("Bloodhound[ERROR]: %s\n", msg)
		}
	}()
	go func() { // why the fuck does this need to be in a thread
		err = containers.Logs(conn, bloodhoundID, lo, bhOut, bhErr)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}()
	<-bhReady

	// update postgresql password expiration
	// TODO
	fmt.Print("Press any key to exit.")
	input := bufio.NewScanner(os.Stdin)
	input.Scan()
}

func SpawnPostgresql(conn *context.Context, wd string) (string, error) {
	// postgresql image
	_, err := images.Pull(*conn, POSTGRESQL, nil)
	if err != nil {
		// fmt.Println(err)
		return "", err
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
		// fmt.Println(err)
		return "", err
	}
	fmt.Println("Container created.")
	if err := containers.Start(*conn, createResponse.ID, nil); err != nil {
		// fmt.Println(err)
		return "", err
	}
	fmt.Println("Container started.")
	return createResponse.ID, nil
}

func SpawnNeo4j(conn *context.Context, wd string) (string, error) {
	// neo4j image
	_, err := images.Pull(*conn, NEO4J, nil)
	if err != nil {
		// fmt.Println(err)
		return "", err
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
		// fmt.Println(err)
		return "", err
	}
	fmt.Println("Container created.")
	if err := containers.Start(*conn, createResponse.ID, nil); err != nil {
		// fmt.Println(err)
		return "", err
	}
	fmt.Println("Container started.")
	return createResponse.ID, nil
}

func SpawnBloodhoundCE(conn *context.Context, wd string) (string, error) {
	// bloodhound image
	_, err := images.Pull(*conn, BLOODHOUND, nil)
	if err != nil {
		// fmt.Println(err)
		return "", err
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
		// fmt.Println(err)
		return "", err
	}
	fmt.Println("Container created.")
	if err := containers.Start(*conn, createResponse.ID, nil); err != nil {
		// fmt.Println(err)
		return "", err
	}
	fmt.Println("Container started.")
	return createResponse.ID, nil
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
