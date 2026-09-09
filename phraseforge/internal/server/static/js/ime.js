// #region: Common utility functions

function sortIme(a,b) {
	return b[0].length - a[0].length
}

function compileImeSuffix(data) {
    data.sort((a, b) => a[0].length - b[0].length)
    const result = new Array()
    for (i in data) {
        let key = data[i][0]
        const value = data[i][1]
        for (j in result) {
            if (key.startsWith(result[j][0])) {
                key = result[j][1] + key.substring(result[j][0].length)
            }
        }
        result.push([key, value])
    }
    return result.sort((a, b) => b[0].length - a[0].length)
}

function compileImeTable(data) { 
    const result = data.reduce((res, item) => (item[0] in res ? res[item[0]].push(item[1]):res[item[0]]=[item[1],], res), {})
    return result
}

function insertText(text) {
    prefix = this.value.substring(0, this.selectionStart)
    suffix = this.value.substring(this.selectionEnd,this.value.length)

    this.value = prefix + text + suffix

    this.selectionStart = this.selectionEnd = this.value.length - suffix.length
}

// #endregion

// #region: Suffix IME

function newStateSuffix() {
    return {shift:false, alt:false, ctrl:false}
}

function onKeyDownSuffix(event) {
	const key = event.key
	switch(key) {
        case 'Shift':
            this.state.shift = true
            break
        case 'Alt':
            this.state.alt = true
            break
        case 'Control':
            this.state.ctrl = true
            break
        case 'Tab':
            this.insertText("\t")
            event.preventDefault()
            break
	}
}

function onKeyUpSuffix(event) {
	const key = event.key
	switch(key) {
        case 'Shift':
            this.state.shift = false
            break
        case 'Alt':
            this.state.alt = false
            break
        case 'Control':
            this.state.ctrl = false
            break
	}
}

function onKeyPressSuffix(event) {
	if (this.ime == null) return
	if (event.key == "Enter") return
	
	event.preventDefault()
	var key = event.key

    for (let i of this.ime) { 
        if (!i[0].endsWith(key)) continue
        const context = i[0].slice(0, -1)
        
        prefix = this.value.substring(0, this.selectionStart)
        if (!prefix.endsWith(context)) continue
        
        this.selectionStart -= context.length
        key = i[1]
    }

    this.insertText(key)
}

// #endregion

// #region: Table IME

function newStateTable() {
    return {composition:"", candidates:[], page:0, next:false}
}

function updateStateTable(state, ime) {
    if (state['composition'] in ime) {
        items = ime[state['composition']]
        state['next'] = items.length > 10 * state['page'] + 10
        if (state['next']) {
            state['candidates'] = items.slice(10 * state['page'], 10 * state['page'] + 10)
        } else {
            state['candidates'] = items.slice(10 * state['page'])
        }
    } else {
        state.candidates = []
        state.page = 0
        state.next = false
    }
}

function formatCandidatesTable(state) {
    if (state['candidates']) {
        items = []

        if (state['page'] > 0) {
            items.push("&mldr;")
        }

        index = 0
        for (const item of state['candidates']) {
            items.push(`${index}=${item}`)
            index++
        }

        if (state['next']) {
            items.push("&mldr;")
        }

        return items.join(" ")
    } else return ""
}

function onKeyDownTable(event) {
	const key = event.key

	switch(key) {
        case 'Tab':
            event.preventDefault()
            this.insertText("\t")
            break
        case 'Escape':
            this.state.composition = ""
            break
        case 'Backspace':
            this.state.composition = this.state.composition.substring(0, this.state.composition.length - 1)
            break
        case 'PageUp':
            if (this.state.page > 0) {
                this.state.page--
            }
            break
        case 'PageDown':
            if (this.state.next) {
                this.state.page++
            }
            break
	}

    updateStateTable(this.state, this.ime)
}

function onKeyUpTable(event) {
	const key = event.key
}

function onKeyPressTable(event) {
	if (this.ime == null) return
	if (this.state.composition == "" && (event.key == "Enter" || event.key == " ")) return
	
	event.preventDefault()
	var key = event.key
    text = undefined
    
    switch (key) {
        case "Enter":
        case " ":
        case "0":
        case "1":
        case "2":
        case "3":
        case "4":
        case "5":
        case "6":
        case "7":
        case "8":
        case "9":
            if (key == "Enter" || key == " ") {
                index = 0
            } else {
                index = parseInt(key)
            }
            if (index < this.state.candidates.length) {
                text = this.state.candidates[index] + this.meta['separator']
            } else {
                text = this.state.composition + key
            }
            this.state.composition = ""
            break

        default:

            if (this.meta.punctuation.includes(key)) {
                text = this.ime[key]
            }

            if (this.meta.alphabet.includes(key)) {
                this.state.composition += key
            }
    }

    updateStateTable(this.state, this.ime)

    if (text) {
        this.insertText(text)
    }
}

// #endregion