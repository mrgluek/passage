//go:build !darwin && !linux && !windows

package main

import "github.com/mrgluek/passage/core/internal/sysdns"

func defaultRoutes() ([]networkRoute, error)   { return nil, nil }
func networkResolvers([]networkRoute) []string { return sysdns.Resolvers() }
