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

func TestRussianStringsComplete(t *testing.T) {
	for key := range texts["en"] {
		if texts["ru"][key] == "" {
			t.Errorf("missing Russian string: %s", key)
		}
	}
}

func TestSettingsEmptyStrings(t *testing.T) {
	for _, tc := range []struct {
		lang, routes, subscriptions, link string
	}{
		{"en", "No routes yet. Paste a link below to add routes.", "No subscriptions. A subscription link keeps your routes up to date by itself: paste it below, and Passage loads the current routes and refreshes them in the background.", "Paste a Passage link to add or replace routes"},
		{"ru", "Маршрутов пока нет. Вставьте ссылку ниже, чтобы добавить маршруты.", "Подписок нет. Ссылка-подписка сама обновляет маршруты: вставьте её ниже, и Passage загрузит актуальные маршруты и будет обновлять их в фоне.", "Вставьте ссылку Passage, чтобы добавить или заменить маршруты"},
	} {
		tr := tr(table(tc.lang))
		for _, check := range []struct{ key, want string }{
			{"saved_routes_empty", tc.routes}, {"subscriptions_empty", tc.subscriptions}, {"link_hint", tc.link},
		} {
			if got := tr.f(check.key); got != check.want {
				t.Errorf("%s/%s = %q, want %q", tc.lang, check.key, got, check.want)
			}
		}
	}
}

func TestStartHint(t *testing.T) {
	for _, tc := range []struct{ lang, want string }{
		{"en", "Routes loaded. Start the proxy."},
		{"ru", "Маршруты загружены. Запустите прокси."},
	} {
		tr := tr(table(tc.lang))
		for _, check := range []struct {
			running bool
			routes  int
			want    string
		}{
			{true, 3, ""},
			{false, 0, ""},
			{false, 3, tc.want},
		} {
			if got := startHint(tr, check.running, check.routes); got != check.want {
				t.Errorf("%s startHint(%v, %d) = %q, want %q", tc.lang, check.running, check.routes, got, check.want)
			}
		}
	}
}

func TestRouteStateWords(t *testing.T) {
	for _, tc := range []struct {
		name, mode, id, state, en, ru string
	}{
		{"auto up", "auto", "a", "up", "connected", "подключён"},
		{"auto idle", "auto", "a", "idle", "standby", "ожидает"},
		{"auto failed", "auto", "a", "failed", "not answering", "не отвечает"},
		{"pinned up", "route:a", "a", "up", "connected", "подключён"},
		{"pinned failed", "route:a", "a", "failed", "not answering", "не отвечает"},
		{"pinned idle", "route:a", "a", "idle", "pinned", "закреплён"},
		{"other idle", "route:a", "b", "idle", "not used", "не используется"},
		{"other up", "route:a", "b", "up", "not used", "не используется"},
		{"other failed", "route:a", "b", "failed", "not used", "не используется"},
	} {
		for _, lang := range []string{"en", "ru"} {
			t.Run(lang+"/"+tc.name, func(t *testing.T) {
				want := tc.en
				if lang == "ru" {
					want = tc.ru
				}
				if got := routeStateName(tr(table(lang)), tc.state, tc.id, tc.mode); got != want {
					t.Fatalf("got %q, want %q", got, want)
				}
			})
		}
	}
}
