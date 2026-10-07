// Texts of the help page that are also used elsewhere (and in tests).

/** Sections of /help in order: id = anchor (/help#students). */
export const HELP_SECTIONS = [
  { id: 'how', title: 'Как это работает' },
  { id: 'prepare', title: 'Подготовка' },
  { id: 'during', title: 'Во время занятия' },
  { id: 'after', title: 'После занятия' },
  { id: 'faq', title: 'Частые вопросы' },
  { id: 'students', title: 'Памятка для студентов' },
] as const

export type HelpSectionId = (typeof HELP_SECTIONS)[number]['id']

/** TZ §33, word for word. */
export const STUDENT_RESPONSIBILITY_TEXT =
  'Для автоматического учёта посещаемости необходимо указывать в Zoom имя и фамилию, соответствующие данным в списке учебной группы. Если имя участника не позволяет однозначно определить студента, система может не учитывать его присутствие. Ответственность за корректное отображаемое имя в Zoom лежит на участнике.'

/** Practical part of the memo: how to set the name in Zoom. */
export const STUDENT_HOWTO_LINES = [
  'Лучше заранее: перед входом в конференцию впишите фамилию и имя в поле «Ваше имя» так же, как в списке группы (например, «Иванов Иван»). Если вы входите с аккаунта Zoom, проверьте имя в профиле — оно подставляется автоматически.',
  'Если вы уже в конференции: «Участники» → наведите на своё имя → «Ещё» → «Переименовать», затем выйдите и зайдите снова по той же ссылке — так преподаватель сразу увидит вас в списке.',
] as const

/** The whole memo as plain text for the clipboard (messenger / e-mail). */
export const STUDENT_MEMO = ['Памятка для студентов', '', STUDENT_RESPONSIBILITY_TEXT, '', ...STUDENT_HOWTO_LINES].join('\n')
