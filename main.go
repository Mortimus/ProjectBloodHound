package main

import (
	"context"
	"fmt"
	"os"

	"github.com/containers/common/libnetwork/types"
	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/bindings/network"
	"github.com/containers/podman/v5/pkg/specgen"
)

const (
	BLOODHOUND  = "docker.io/specterops/bloodhound"
	NEO4J       = "docker.io/library/neo4j:4.4"
	POSTGRESQL  = "docker.io/library/postgres:16"
	NETWORK     = "BloodHound-CE-network"
	PSQLFOLDER  = "bloodhound-data/postgresql"
	NEO4JFOLDER = "bloodhound-data/neo4j"
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
	conn, err := bindings.NewConnection(context.Background(), "unix:///run/podman/podman.sock")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	// Pull bloodhound, postsgresql, and neo4j images
	_, err = images.Pull(conn, BLOODHOUND, nil)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	// neo4j image
	_, err = images.Pull(conn, NEO4J, nil)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	// postgresql image
	_, err = images.Pull(conn, POSTGRESQL, nil)
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
			Name: NETWORK,
		})
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}
	// Create a POSTGRES container

	s := specgen.NewSpecGenerator(BLOODHOUND, false)
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
			Source:      PSQLFOLDER,                 // local folder
			Destination: "/var/lib/postgresql/data", // container folder
		},
	}
	remove := true
	s.Remove = &remove

	createResponse, err := containers.CreateWithSpec(conn, s, nil)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Container created.")
	if err := containers.Start(conn, createResponse.ID, nil); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Container started.")
	inspectData, err := containers.Inspect(conn, createResponse.ID, new(containers.InspectOptions).WithSize(true))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	// Print the container ID
	fmt.Println(inspectData.ID)
	// stop container
	if err := containers.Stop(conn, createResponse.ID, nil); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Container stopped.")
	// remove container
	if err := containers.Remove(conn, createResponse.ID, nil); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Println("Container removed.")
}
