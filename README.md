# ProjectBloodHound
a tool for spinning up BloodHound CE containers and configuring common tools for project based activities.

## Requirements
`podman`
`libbtrfs-dev`
`libgpgme-dev`

## Arguments

```
Usage of ProjectBloodHound:
  -custom string
        Path to custom queries json file in legacy bloodhound format (default "customqueries.json")
  -expiration string
        Set password expiration date (default "2035-02-16 16:51:44")
  -force
        Force injection of custom queries - will add duplicates if they already exist
  -logs
        Show container logs
  -path string
        Path to store data folders (default "/home/Mortimus/")
  -pull
        Pull images before starting containers
```