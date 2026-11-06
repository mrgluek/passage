package main

import (
	"fmt"
	"testing"
)

func TestSubscriptionQuantities(t *testing.T) {
	cases := []struct {
		count   int64
		russian int
	}{
		{0, 2}, {1, 0}, {2, 1}, {4, 1}, {5, 2}, {11, 2}, {12, 2},
		{14, 2}, {21, 0}, {22, 1}, {25, 2}, {101, 0}, {111, 2},
	}
	for _, lang := range []string{"en", "ru"} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/%d", lang, tc.count), func(t *testing.T) {
				tr := tr(table(lang))
				route, address, days := "routes", "addresses", "days"
				detail, age := "revision 1 · %d %s", "updated %d %s ago"
				if lang == "ru" {
					route = []string{"маршрут", "маршрута", "маршрутов"}[tc.russian]
					address = []string{"адрес", "адреса", "адресов"}[tc.russian]
					days, detail, age = "дн.", "Версия 1 · %d %s", "обновлено %d %s назад"
				} else if tc.count == 1 {
					route, address, days = "route", "address", "day"
				}
				for _, check := range []struct{ got, want string }{
					{tr.quantity("sub_detail", tc.count, 1, tc.count), fmt.Sprintf(detail, tc.count, route)},
					{tr.quantity("sub_addresses", tc.count, tc.count), fmt.Sprintf("%d %s", tc.count, address)},
					{tr.quantity("sub_days", tc.count, tc.count), fmt.Sprintf(age, tc.count, days)},
				} {
					if check.got != check.want {
						t.Errorf("got %q, want %q", check.got, check.want)
					}
				}
			})
		}
	}
}
