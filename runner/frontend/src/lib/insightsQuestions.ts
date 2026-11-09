export const QUESTIONS = [
 {id:'health', label:'Agent health', question:'Are my agents working well?'},
 {id:'cost', label:'Card cost', question:'What does a card really cost?'},
 {id:'time', label:'Time', question:'Where does the time go?'},
 {id:'spending', label:'Spending', question:'What am I spending?'},
 {id:'skills', label:'Skills', question:'Are my skills helping?'},
 {id:'context', label:'Context', question:'How full is my context?'},
] as const
export type QuestionView = (typeof QUESTIONS)[number]['id']
