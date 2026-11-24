package alerts

import (
	"fmt"
	"strings"
	"time"
)

// The sentences people read. They are Russian, like the interface; another
// language is another set of these functions.

// plural picks the Russian form for a count: 1 минута, 2 минуты, 5 минут.
func plural(n int64, one, few, many string) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod10 == 1 && mod100 != 11:
		return one
	case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
		return few
	}
	return many
}

// duration says a length of time the way one would aloud: "14 минут",
// "1 час 52 минуты", "2 дня 3 часа".
func duration(d time.Duration) string {
	minutes := int64(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	days, hours, mins := minutes/(24*60), minutes/60%24, minutes%60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", days, plural(days, "день", "дня", "дней")))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", hours, plural(hours, "час", "часа", "часов")))
	}
	// Minutes of a stretch counted in days are noise.
	if mins > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%d %s", mins, plural(mins, "минута", "минуты", "минут")))
	}
	return strings.Join(parts, " ")
}

func clock(t time.Time) string { return t.Local().Format("15:04") }

func textDown(server string, since time.Time, now time.Time) string {
	return fmt.Sprintf("Сервер %s не отвечает на проверки с %s (%s).", server, clock(since), duration(now.Sub(since)))
}

func textStillDown(server string, since time.Time, now time.Time) string {
	return fmt.Sprintf("Сервер %s всё ещё недоступен: с %s, уже %s.", server, clock(since), duration(now.Sub(since)))
}

func textUp(server string, down time.Duration) string {
	return fmt.Sprintf("Сервер %s снова в сети. Был недоступен %s.", server, duration(down))
}

func textUpdate(version string, servers []string) string {
	return fmt.Sprintf("Вышел Xray %s. На прежней версии: %s.", version, strings.Join(servers, ", "))
}

// textCert says a certificate was not renewed: how long it still has, or that
// it has run out.
func textCert(server, domain string, notAfter, now time.Time) string {
	left := notAfter.Sub(now)
	if left <= 0 {
		return fmt.Sprintf("Сертификат %s на сервере %s истёк %s и не продлён. Посетители домена видят предупреждение браузера.",
			domain, server, notAfter.Local().Format("02.01.2006"))
	}
	days := int64(left / (24 * time.Hour))
	if days < 1 {
		return fmt.Sprintf("Сертификат %s на сервере %s истекает сегодня и не продлён.", domain, server)
	}
	return fmt.Sprintf("Сертификат %s на сервере %s не продлён: действует до %s, %s %d %s.",
		domain, server, notAfter.Local().Format("02.01.2006"),
		plural(days, "остался", "осталось", "осталось"), days, plural(days, "день", "дня", "дней"))
}

const textTest = "Проверочное сообщение Xray Console: оповещения настроены."

// digest joins several messages into one, each with the time it is about.
func digest(messages []Message) string {
	if len(messages) == 1 {
		return messages[0].Text
	}
	lines := []string{fmt.Sprintf("Xray Console, %d %s:", len(messages),
		plural(int64(len(messages)), "событие", "события", "событий"))}
	for _, m := range messages {
		lines = append(lines, clock(m.At)+" — "+m.Text)
	}
	return strings.Join(lines, "\n")
}
