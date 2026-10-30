// The clean fixture: six letter pages, one layout each. Every page's text is data first: the page
// sets it, and a <truth> metadata element records the same text in reading order, which
// generate.sh queries into the page's ground truth. Hyphenation and justification are off, so
// every word on the page is a whole word of the source.
#set document(title: "document fixtures", date: none)
#set page(paper: "us-letter", margin: 1in)
#set text(font: "Libertinus Serif", size: 12pt, hyphenate: false, lang: "en")
#set par(justify: false)

// truth records a page's ground truth: its blocks in reading order, one blank line between.
#let truth(blocks) = [#metadata(blocks.join("\n\n")) <truth>]

// Page 1: prose.
#let prose-title = "The Harbor Survey"
#let prose = (
  "The harbor survey began in the spring, when the ice had cleared from the inner channel and the pilots could take the launch out at first light. Each morning the crew measured the depth at forty marked stations, wrote the readings in a bound log, and compared them with the charts drawn twenty years before.",
  "By the middle of summer the pattern was plain. The channel had shifted toward the eastern breakwater, and the shoal off the old ferry landing had grown by almost a meter. The pilots had known this for years from the feel of the boats, but the survey gave them numbers they could show the port council.",
  "The council voted in the autumn to dredge the eastern approach and to move two of the channel buoys. The survey crew stayed on through the winter to check the work, and their final log, bound in green cloth, is kept in the harbor office today.",
)
#heading(level: 1, prose-title)
#for p in prose [#p #parbreak()]
#truth((prose-title,) + prose)
#pagebreak()

// Page 2: two columns.
#let columns-title = "Notes on Garden Soil"
#let left-column = (
  "Good garden soil holds water without staying wet. A handful squeezed in the palm should keep its shape for a moment and then crumble when pressed with a thumb.",
  "Clay soil drains slowly and warms late in the spring. Working in leaf mold and coarse sand over several seasons opens it up and lets roots reach deeper.",
  "Sandy soil drains quickly and loses its nutrients in heavy rain. Compost added each autumn gives it body and feeds the worms that keep it loose.",
)
#let right-column = (
  "Most vegetables grow best in soil that is slightly acid. A simple test kit from the hardware store shows the level in a few minutes.",
  "Lime raises the level slowly and should go on in the autumn, so the winter rain can carry it down. Sulfur lowers it, but more slowly still.",
  "Whatever the soil, a layer of mulch keeps it cool in summer, holds moisture through dry weeks, and smothers most of the weeds before they start.",
)
#heading(level: 1, columns-title)
#columns(2, gutter: 0.4in)[
  #for p in left-column [#p #parbreak()]
  #colbreak()
  #for p in right-column [#p #parbreak()]
]
#truth((columns-title,) + left-column + right-column)
#pagebreak()

// Page 3: a table. Its ground truth reads row by row, each row's cells joined by single spaces.
#let table-title = "Quarterly Rainfall by Station"
#let table-rows = (
  ("Station", "Winter", "Spring", "Summer", "Autumn"),
  ("North Ridge", "212 mm", "148 mm", "64 mm", "190 mm"),
  ("Mill Creek", "187 mm", "131 mm", "58 mm", "176 mm"),
  ("Lower Valley", "143 mm", "102 mm", "41 mm", "129 mm"),
  ("Harbor Point", "236 mm", "159 mm", "77 mm", "214 mm"),
  ("Cedar Flats", "98 mm", "86 mm", "35 mm", "91 mm"),
  ("East Summit", "254 mm", "171 mm", "82 mm", "233 mm"),
)
#let table-note = "Readings are totals for each quarter, rounded to the nearest millimeter."
#heading(level: 1, table-title)
#table(
  columns: 5,
  inset: 8pt,
  align: (left, right, right, right, right),
  ..table-rows.flatten(),
)
#table-note
#truth((table-title,) + table-rows.map(r => r.join(" ")) + (table-note,))
#pagebreak()

// Page 4: 8 pt print.
#let small-title = "Terms of Service"
#let small = (
  "1. These terms apply to every rental of equipment from the shop, whether booked in person, by telephone, or through the website. By taking the equipment the renter accepts these terms in full.",
  "2. The renter must return the equipment by the time written on the rental slip. Equipment returned late is charged at the daily rate for each day or part of a day after that time.",
  "3. The renter is responsible for any damage beyond ordinary wear. The shop will inspect the equipment on its return and send a written estimate of any repair within five working days.",
  "4. A deposit is held for the length of the rental and returned in full when the equipment comes back clean, complete, and in working order. Missing parts are charged at their replacement cost.",
  "5. The shop may refuse a rental, or end one early, when the equipment is used in a way that puts people or property at risk. In that case the unused days are refunded.",
  "6. Bookings cancelled at least two days before the start of the rental are refunded in full. Later cancellations are charged one day at the daily rate.",
  "7. The shop keeps the renter's name, address, and telephone number for as long as the rental lasts and for one year after, and shares them with no one except as the law requires.",
)
#heading(level: 1, small-title)
#text(size: 8pt)[#for p in small [#p #parbreak()]]
#truth((small-title,) + small)
#pagebreak()

// Page 5: a form, filled in. Its ground truth reads each field as its label, a colon, and its
// value.
#let form-title = "Library Card Application"
#let form-fields = (
  ("Full name", "Margaret Ellen Doyle"),
  ("Street address", "41 Orchard Lane"),
  ("City", "Fairhaven"),
  ("Postal code", "04821"),
  ("Telephone", "555 0142"),
  ("Date of birth", "12 March 1987"),
  ("Card type", "Adult"),
)
#let form-signature = "Signed by the applicant on 3 June 2026."
#heading(level: 1, form-title)
#v(0.2in)
#for (label, value) in form-fields [
  #grid(
    columns: (1.8in, 1fr),
    align: bottom,
    [#label:],
    box(width: 100%, stroke: (bottom: 0.6pt), inset: (bottom: 4pt))[#value],
  )
  #v(0.25in)
]
#form-signature
#truth((form-title,) + form-fields.map(((l, v)) => l + ": " + v) + (form-signature,))
#pagebreak()

// Page 6: a figure with a caption. The figure is shapes only, so the caption is its only text.
#let figure-title = "Seasonal Water Levels"
#let figure-intro = "The reservoir rises through the wet months and falls through the dry ones. The chart below shows the average level at the end of each season over the last ten years."
#let figure-caption = "Average reservoir level at the end of winter, spring, summer, and autumn."
#let figure-after = "The lowest level comes at the end of summer, when demand is highest and the inflow is smallest."
#heading(level: 1, figure-title)
#figure-intro
#figure(
  box(width: 4in, height: 2.4in, stroke: 0.6pt, inset: 12pt)[
    #set align(bottom)
    #stack(
      dir: ltr,
      spacing: 0.35in,
      ..(1.8in, 1.4in, 0.6in, 1.1in).map(h => rect(width: 0.5in, height: h, fill: luma(110))),
    )
  ],
  caption: figure-caption,
)
#figure-after
#truth((figure-title, figure-intro, "Figure 1: " + figure-caption, figure-after))
