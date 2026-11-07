package ai

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/MaramHarsha/MangaVeyra/internal/pagedoc"
)

const defaultPlaceholderPages = 8

// placeholderCopy is the template script in one content language. All of it
// is original MangaVeyra copy. lines follow placeholderBeats; named holds the
// variants of lines 7 and 10 that address the other character by name.
type placeholderCopy struct {
	summary, chapter, action, notes, timeOfDay, camera, shot, emotion string
	lines                                                             [14]string
	named                                                             [2]string
}

// placeholderBeat places template line idx: who is "a" (the panel's
// character), "b" (the next character, or an unnamed voice when there is
// none) or "" for a caption; named indexes placeholderCopy.named or is -1.
type placeholderBeat struct {
	idx       int
	kind, who string
	named     int
}

// Eight beats cycle through the chapter, mixing speech, thought and captions.
var placeholderBeats = [8][]placeholderBeat{
	{{0, "caption", "", -1}, {1, "speech", "a", -1}},
	{{2, "speech", "a", -1}},
	{{3, "thought", "a", -1}, {4, "speech", "b", -1}},
	{{5, "caption", "", -1}, {6, "speech", "a", -1}},
	{{7, "speech", "b", 0}, {8, "speech", "a", -1}},
	{{9, "thought", "a", -1}},
	{{10, "speech", "a", 1}, {11, "caption", "", -1}},
	{{12, "speech", "b", -1}, {13, "thought", "a", -1}},
}

var placeholderCopies = map[string]placeholderCopy{
	"en": {"An original MangaVeyra chapter begins.", "Chapter %d", "Placeholder panel %d of %d. %s", "Editable offline placeholder.", "unspecified", "storyboard", "placeholder", "neutral",
		[14]string{"It began on an ordinary morning.", "Did you hear that?", "We have to keep going.", "Something feels different today…", "Stay close to me.", "Later that day…", "Look over there!",
			"Do you really want to do this?", "No. But let's go anyway!", "I can't turn back now.", "Wait for me!", "The wind began to change.", "We made it.", "This is only the beginning."},
		[2]string{"%s, do you really want to do this?", "%s, wait for me!"}},
	"ja": {"MangaVeyraのオリジナル章が始まる。", "第%d話", "仮のコマ %d/%d。%s", "オフラインの仮原稿です。編集できます。", "未定", "絵コンテ", "仮", "平静",
		[14]string{"それは、いつもの朝に始まった。", "今の、聞こえた？", "このまま進むしかない。", "今日は何かが違う気がする……", "私から離れないで。", "その日の午後――", "あそこを見て！",
			"本当にやるの？", "ううん。でも行こう！", "もう引き返せない。", "待って！", "風向きが変わり始めた。", "やっと着いた。", "これは、まだ始まりにすぎない。"},
		[2]string{"%s、本当にやるの？", "%s、待って！"}},
	"ko": {"MangaVeyra의 새로운 오리지널 챕터가 시작된다.", "제%d화", "임시 컷 %d/%d. %s", "오프라인 임시 대본입니다. 편집할 수 있습니다.", "미정", "콘티", "임시", "담담함",
		[14]string{"평범한 아침, 모든 것이 시작되었다.", "방금 그 소리 들었어?", "계속 가야 해.", "오늘은 뭔가 달라.", "내 곁에 있어.", "그날 오후…", "저기 봐!",
			"정말 할 거야?", "아니. 그래도 가자!", "이제 돌아갈 수 없어.", "기다려!", "바람의 방향이 바뀌기 시작했다.", "드디어 도착했어.", "이건 시작일 뿐이야."},
		[2]string{"%s, 정말 할 거야?", "%s, 기다려!"}},
	"zh-Hans": {"一个原创的 MangaVeyra 章节开始了。", "第%d章", "占位分镜 %d/%d。%s", "离线占位稿，可以编辑。", "未定", "分镜", "占位", "平静",
		[14]string{"一切始于一个平常的早晨。", "你听到了吗？", "我们得继续前进。", "今天好像有些不一样……", "跟紧我。", "那天下午……", "快看那边！",
			"你真的要这么做吗？", "不想。但还是走吧！", "已经回不了头了。", "等等我！", "风向开始变了。", "我们做到了。", "这只是开始。"},
		[2]string{"%s，你真的要这么做吗？", "%s，等等我！"}},
	"zh-Hant": {"一個原創的 MangaVeyra 章節開始了。", "第%d章", "佔位分鏡 %d/%d。%s", "離線佔位稿，可以編輯。", "未定", "分鏡", "佔位", "平靜",
		[14]string{"一切始於一個平常的早晨。", "你聽到了嗎？", "我們得繼續前進。", "今天好像有些不一樣……", "跟緊我。", "那天下午……", "快看那邊！",
			"你真的要這麼做嗎？", "不想。但還是走吧！", "已經回不了頭了。", "等等我！", "風向開始變了。", "我們做到了。", "這只是開始。"},
		[2]string{"%s，你真的要這麼做嗎？", "%s，等等我！"}},
	"es": {"Comienza un capítulo original de MangaVeyra.", "Capítulo %d", "Viñeta provisional %d de %d. %s", "Borrador provisional sin conexión; se puede editar.", "sin especificar", "guion gráfico", "provisional", "neutral",
		[14]string{"Todo empezó una mañana cualquiera.", "¿Has oído eso?", "Tenemos que seguir adelante.", "Hoy algo se siente distinto…", "Quédate cerca de mí.", "Esa misma tarde…", "¡Mira allí!",
			"¿De verdad quieres hacerlo?", "No. ¡Pero vamos de todos modos!", "Ya no puedo dar marcha atrás.", "¡Espérame!", "El viento empezó a cambiar.", "Lo logramos.", "Esto es solo el principio."},
		[2]string{"%s, ¿de verdad quieres hacerlo?", "¡%s, espérame!"}},
	"fr": {"Un chapitre original de MangaVeyra commence.", "Chapitre %d", "Case provisoire %d sur %d. %s", "Brouillon provisoire hors ligne, modifiable.", "non précisé", "storyboard", "provisoire", "neutre",
		[14]string{"Tout a commencé un matin comme les autres.", "Tu as entendu ça ?", "Il faut continuer.", "Quelque chose a changé aujourd'hui…", "Reste près de moi.", "Plus tard ce jour-là…", "Regarde, là-bas !",
			"Tu veux vraiment faire ça ?", "Non. Mais allons-y quand même !", "Je ne peux plus faire demi-tour.", "Attends-moi !", "Le vent commença à tourner.", "On a réussi.", "Ce n'est que le début."},
		[2]string{"%s, tu veux vraiment faire ça ?", "%s, attends-moi !"}},
	"de": {"Ein neues MangaVeyra-Kapitel beginnt.", "Kapitel %d", "Platzhalter-Panel %d von %d. %s", "Bearbeitbarer Offline-Platzhalter.", "unbestimmt", "Storyboard", "Platzhalter", "neutral",
		[14]string{"Alles begann an einem gewöhnlichen Morgen.", "Hast du das gehört?", "Wir müssen weiter.", "Heute fühlt sich etwas anders an …", "Bleib dicht bei mir.", "Später an diesem Tag …", "Sieh mal dort!",
			"Willst du das wirklich?", "Nein. Aber los geht's trotzdem!", "Jetzt kann ich nicht mehr umkehren.", "Warte auf mich!", "Der Wind begann zu drehen.", "Wir haben es geschafft.", "Das ist erst der Anfang."},
		[2]string{"%s, willst du das wirklich?", "%s, warte auf mich!"}},
	"pt": {"Começa um capítulo original de MangaVeyra.", "Capítulo %d", "Quadro provisório %d de %d. %s", "Rascunho provisório offline; pode ser editado.", "não especificado", "storyboard", "provisório", "neutro",
		[14]string{"Tudo começou numa manhã comum.", "Você ouviu isso?", "Precisamos continuar.", "Hoje algo parece diferente…", "Fique perto de mim.", "Mais tarde, naquele dia…", "Olhe ali!",
			"Você quer mesmo fazer isso?", "Não. Mas vamos mesmo assim!", "Agora não posso voltar atrás.", "Espere por mim!", "O vento começou a mudar.", "Conseguimos.", "Isto é só o começo."},
		[2]string{"%s, você quer mesmo fazer isso?", "%s, espere por mim!"}},
	"hi": {"MangaVeyra का एक मौलिक अध्याय शुरू होता है।", "अध्याय %d", "अस्थायी पैनल %d/%d। %s", "ऑफ़लाइन अस्थायी पटकथा; इसे संपादित किया जा सकता है।", "अनिर्दिष्ट", "स्टोरीबोर्ड", "अस्थायी", "सामान्य",
		[14]string{"सब कुछ एक आम सुबह शुरू हुआ।", "क्या तुमने वह सुना?", "हमें आगे बढ़ते रहना होगा।", "आज कुछ अलग-सा लग रहा है…", "मेरे पास ही रहना।", "उसी दिन बाद में…", "वहाँ देखो!",
			"क्या सच में यही करना है?", "नहीं। फिर भी चलो!", "अब पीछे मुड़ना मुमकिन नहीं।", "मेरा इंतज़ार करो!", "हवा का रुख़ बदलने लगा।", "हम पहुँच गए।", "यह तो बस शुरुआत है।"},
		[2]string{"%s, क्या सच में यही करना है?", "%s, मेरा इंतज़ार करो!"}},
}

func placeholderCopyFor(lang string) placeholderCopy {
	if c, ok := placeholderCopies[pagedoc.NormalizeContentLanguage(lang)]; ok {
		return c
	}
	return placeholderCopies["en"]
}

// generatePlaceholderScript is intentionally free of clocks, randomness and
// database identifiers. Normalized input therefore produces byte-identical
// output on every host. The script is written in the project's content
// language and every panel carries one or two short original lines (speech,
// thought or caption, spoken by the project's characters when there are any),
// so offline "Make pages" letters real bubbles.
func generatePlaceholderScript(in ScriptInput) (json.RawMessage, error) {
	tpl := placeholderCopyFor(in.Language)
	pages := in.PagesTarget
	if pages == 0 {
		pages = defaultPlaceholderPages
	}
	source := in.Premise
	if in.Mode == "adapt" {
		source = in.SourceText
	}
	summary := runePrefix(strings.TrimSpace(source), 4000)
	if summary == "" {
		summary = tpl.summary
	}
	title := strings.TrimSpace(in.ChapterTitle)
	if title == "" {
		title = fmt.Sprintf(tpl.chapter, in.ChapterNumber)
	}
	scene := scriptScene{Key: "scene-1", Title: runePrefix(title, 160), Summary: summary,
		TimeOfDay: tpl.timeOfDay, Panels: make([]scriptPanel, 0, pages)}
	for page := 1; page <= pages; page++ {
		// Keep the repeated excerpt byte-bounded. At 256 pages, repeating the
		// full 4,000-rune summary (especially four-byte Unicode) could otherwise
		// push a valid offline result beyond the 1 MiB document ceiling.
		action := fmt.Sprintf(tpl.action, page, pages, utf8BytePrefix(summary, 512))
		panel := scriptPanel{Key: fmt.Sprintf("panel-%d", page), PageNumber: page, Camera: tpl.camera,
			Shot: tpl.shot, Emotion: tpl.emotion, Action: runePrefix(action, 2000),
			Dialogue: []scriptDialogue{}, SFX: []scriptSFX{}, CharacterIDs: []string{}, Notes: tpl.notes}
		var a, b *ScriptCharacter
		if n := len(in.Characters); n > 0 {
			a = &in.Characters[(page-1)%n]
			if n > 1 {
				b = &in.Characters[page%n]
			}
		}
		for _, beat := range placeholderBeats[(page-1)%len(placeholderBeats)] {
			d := scriptDialogue{Kind: beat.kind, Text: tpl.lines[beat.idx]}
			speaker, other := a, b
			if beat.who == "b" {
				speaker, other = b, a
			}
			if beat.who != "" && speaker != nil {
				d.Speaker, d.CharacterID = runePrefix(speaker.Name, 160), speaker.ID
				addCharacter(&panel, speaker.ID)
			}
			if beat.named >= 0 && other != nil && other != speaker && strings.TrimSpace(other.Name) != "" {
				d.Text = fmt.Sprintf(tpl.named[beat.named], runePrefix(strings.TrimSpace(other.Name), 160))
			}
			panel.Dialogue = append(panel.Dialogue, d)
		}
		scene.Panels = append(scene.Panels, panel)
	}
	doc := scriptDocument{Schema: SchemaScriptV1, ChapterSummary: summary, Scenes: []scriptScene{scene}}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal placeholder: %w", err)
	}
	_, normalized, err := decodeScriptDocument(raw, in)
	if err != nil {
		return nil, fmt.Errorf("ai: validate placeholder: %w", err)
	}
	return normalized, nil
}

func addCharacter(panel *scriptPanel, id string) {
	for _, existing := range panel.CharacterIDs {
		if existing == id {
			return
		}
	}
	panel.CharacterIDs = append(panel.CharacterIDs, id)
}

func runePrefix(s string, max int) string {
	runes := []rune(s)
	if len(runes) > max {
		runes = runes[:max]
	}
	return string(runes)
}

func utf8BytePrefix(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
