// The guided tour.
//
// A first-run walk through the dashboard, one small card per idea. It is a list
// of steps and nothing else: adding a feature means adding an entry, and the
// runner already knows how to skip a step whose target is not on screen, hide
// the advanced ones in simple mode, switch to the view a step lives on — the
// dashboard, unless the step names another — and remember how far a person got.
//
// Progress is a version number, not a bookmark. Bumping TOUR_VERSION when steps
// are added re-offers the tour to anyone who has seen an older one, which is
// exactly what "and it grows as features are added" means — without a migration
// and without asking the database to remember a step index nobody reads.

import { t } from './i18n.js';

/**
 * TOUR_VERSION rises whenever a step is added. An installation that has seen a
 * lower number is offered the tour again.
 *
 * 3 added the layout switcher and the account menu.
 */
export const TOUR_VERSION = 3;

/**
 * The three things the tour can go into more deeply at the end. The person
 * picks one and the steps carrying that topic are the ones that follow; the
 * list stays linear and the branching is a single variable.
 */
const TOPICS = ['sensors', 'incidents', 'alerts'];

// samples reports whether the installation still has sensors it invented itself.
// It decides both what the welcome and the take-one-apart steps say and which card
// they point at, and it reads the same count the Example data panel is built from,
// so the tour and the panel cannot disagree about whether there is sample data.
const samples = (state) => (state.exampleData?.sensors || 0) > 0;

// cardPart is the sensor the tour takes apart. It is the first invented one while
// the installation still has sample data, and otherwise the first card there is —
// so the words and the sensor they are about agree, which is the difference between
// explaining a part and pointing at somebody's router and calling it a fake.
const cardPart = (part) => ({ state }) =>
  `#groups .card${samples(state) ? '[data-origin="example"]' : ''}${part}`;

const STEPS = [
  {
    id: 'welcome',
    target: null,
    titleKey: 'tour.welcome.title',
    bodyKey: ({ state }) => (samples(state) ? 'tour.welcome.body' : 'tour.welcome.bodyOwn'),
  },
  {
    id: 'summary',
    target: '#summary',
    placement: 'bottom',
    titleKey: 'tour.summary.title',
    bodyKey: 'tour.summary.body',
  },
  // The first card, taken apart one piece at a time. Everything below names the
  // same sensor so that the parts add up to a whole instead of being five
  // unrelated remarks.
  {
    id: 'example-intro',
    layout: 'grid',
    target: cardPart(''),
    titleKey: 'tour.exampleIntro.title',
    bodyKey: ({ state }) => (samples(state) ? 'tour.exampleIntro.body' : 'tour.exampleIntro.bodyOwn'),
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'part-name',
    layout: 'grid',
    target: cardPart(' .card-head'),
    placement: 'bottom',
    titleKey: 'tour.partName.title',
    bodyKey: 'tour.partName.body',
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'part-target',
    layout: 'grid',
    target: cardPart(' .card-target'),
    placement: 'bottom',
    titleKey: 'tour.partTarget.title',
    bodyKey: 'tour.partTarget.body',
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'part-message',
    layout: 'grid',
    target: cardPart(' .card-message'),
    placement: 'bottom',
    titleKey: 'tour.partMessage.title',
    bodyKey: 'tour.partMessage.body',
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'part-spark',
    layout: 'grid',
    target: cardPart(' .card-spark'),
    placement: 'top',
    titleKey: 'tour.partSpark.title',
    bodyKey: 'tour.partSpark.body',
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'part-stats',
    layout: 'grid',
    target: cardPart(' .card-stats'),
    placement: 'top',
    titleKey: 'tour.partStats.title',
    bodyKey: 'tour.partStats.body',
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'part-actions',
    layout: 'grid',
    target: cardPart(' .card-actions'),
    placement: 'top',
    titleKey: 'tour.partActions.title',
    bodyKey: 'tour.partActions.body',
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'detail',
    layout: 'grid',
    target: cardPart(''),
    placement: 'top',
    titleKey: 'tour.detail.title',
    bodyKey: 'tour.detail.body',
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'new-sensor',
    target: '#new-sensor',
    placement: 'bottom',
    panel: true,
    titleKey: 'tour.newSensor.title',
    bodyKey: 'tour.newSensor.body',
  },
  {
    id: 'tabs',
    target: '#tabs',
    placement: 'bottom',
    titleKey: 'tour.tabs.title',
    bodyKey: 'tour.tabs.body',
  },
  // The two controls the top bar grew: how the sensors are laid out, and who is
  // signed in. Both live on the dashboard and nowhere else, which is also where
  // the preceding step has just left us.
  {
    id: 'layout',
    target: '#layout-switcher',
    placement: 'bottom',
    panel: true,
    titleKey: 'tour.layout.title',
    bodyKey: 'tour.layout.body',
  },
  {
    id: 'user-menu',
    target: '#user-menu-toggle',
    placement: 'bottom',
    titleKey: 'tour.userMenu.title',
    bodyKey: 'tour.userMenu.body',
  },
  // Settings lives on another view, and the tour is the only thing that ever
  // walks somebody through it — so a step may say which view it belongs to, and
  // which of its categories, because only one of them is on screen at a time.
  {
    id: 'settings-examples',
    route: 'settings',
    category: 'monitor',
    target: '#examples-panel',
    placement: 'top',
    titleKey: 'tour.settingsExamples.title',
    bodyKey: 'tour.settingsExamples.body',
    when: ({ state }) => (state.exampleData?.sensors || 0) > 0,
  },
  {
    id: 'settings-users',
    route: 'settings',
    category: 'access',
    target: '#users-panel',
    placement: 'top',
    // Only worth a step once accounts and the user list are part of the
    // interface — both for the person (admin) and for the mode.
    mode: 'advanced',
    titleKey: 'tour.settingsUsers.title',
    bodyKey: 'tour.settingsUsers.body',
    when: ({ state }) => state.session?.user?.role === 'admin',
  },
  {
    id: 'choose',
    route: 'dashboard',
    target: null,
    titleKey: 'tour.choose.title',
    bodyKey: 'tour.choose.body',
    choice: TOPICS,
  },
  // Whatever was picked above. A step whose topic is not the chosen one is not
  // a step, which is the whole of the branching.
  {
    id: 'topic-sensors',
    topic: 'sensors',
    target: '#groups',
    placement: 'top',
    titleKey: 'tour.topicSensors.title',
    bodyKey: 'tour.topicSensors.body',
    when: ({ state }) => state.sensors.length > 0,
  },
  {
    id: 'topic-sensors-add',
    topic: 'sensors',
    target: '#new-sensor',
    placement: 'bottom',
    panel: true,
    titleKey: 'tour.topicSensorsAdd.title',
    bodyKey: 'tour.topicSensorsAdd.body',
  },
  {
    id: 'topic-incidents',
    topic: 'incidents',
    route: 'incidents',
    target: '#incident-list',
    placement: 'top',
    titleKey: 'tour.topicIncidents.title',
    bodyKey: 'tour.topicIncidents.body',
  },
  {
    id: 'topic-alerts',
    topic: 'alerts',
    route: 'channels',
    target: '#channel-list',
    placement: 'top',
    titleKey: 'tour.topicAlerts.title',
    bodyKey: 'tour.topicAlerts.body',
  },
  {
    id: 'topic-alerts-attach',
    topic: 'alerts',
    route: 'channels',
    target: '#new-channel',
    placement: 'bottom',
    mode: 'advanced',
    titleKey: 'tour.topicAlertsAttach.title',
    bodyKey: 'tour.topicAlertsAttach.body',
  },
  {
    id: 'done',
    target: null,
    titleKey: 'tour.done.title',
    bodyKey: 'tour.done.body',
  },
];

function isVisible(el) {
  const rect = el.getBoundingClientRect();
  return rect.width > 0 && rect.height > 0;
}

/**
 * The steps that apply right now, in order. Filtering looks at the mode, the
 * state and the chosen topic — never at the DOM, because the target may well
 * live on a view the tour has not switched to yet.
 */
function activeSteps(state, mode, topic) {
  const out = [];
  for (const step of STEPS) {
    if (step.mode === 'advanced' && mode !== 'advanced') continue;
    if (step.topic && step.topic !== topic) continue;
    if (step.when && !step.when({ state })) continue;
    out.push(step);
  }
  return out;
}

/**
 * waitFor gives a view time to arrive. Switching views draws synchronously but
 * then loads its data, so Settings in particular is empty for a frame or two.
 * A step whose target never turns up is still shown, centred, rather than
 * dropped — the words are worth reading on their own.
 */
async function waitFor(selector) {
  for (let frame = 0; frame < 120; frame += 1) {
    const el = document.querySelector(selector);
    if (el && isVisible(el)) return el;
    await new Promise((resolve) => requestAnimationFrame(resolve));
  }
  return null;
}

/**
 * bringIntoView scrolls a target into sight before it is ringed. The dashboard is
 * longer than the window and a card near the bottom is nowhere near the top, so
 * without this the ring would be drawn around something nobody can see — and
 * "nearest" leaves the page alone whenever the target is already visible.
 */
function bringIntoView(el) {
  el?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}

function place(card, ring, placement) {
  card.style.transform = '';
  if (!ring) {
    card.style.left = '50%';
    card.style.top = '50%';
    card.style.transform = 'translate(-50%, -50%)';
    return;
  }
  // Measure at a fixed origin, then move: the card's own size must not depend on
  // where it currently sits.
  card.style.left = '0px';
  card.style.top = '0px';
  const box = card.getBoundingClientRect();
  const at = ring.getBoundingClientRect();
  const margin = 14;

  let left = at.left + at.width / 2 - box.width / 2;
  if (left + box.width > window.innerWidth - margin) left = window.innerWidth - margin - box.width;
  if (left < margin) left = margin;

  let top = placement === 'top' ? at.top - box.height - 12 : at.bottom + 12;
  if (top < margin) top = at.bottom + 12;
  // The viewport has the last word, not the ring. A card that ends up below the
  // fold is a card nobody can read and a Next button nobody can press — and a
  // step about the last card on a long dashboard lands exactly there.
  if (top + box.height > window.innerHeight - margin) top = window.innerHeight - margin - box.height;
  if (top < margin) top = margin;

  card.style.left = `${left}px`;
  card.style.top = `${top}px`;
}

/**
 * start runs the tour. `setRoute` is the dashboard's own view switcher, handed
 * in so that a step can be about a screen the tour has to open first, `setLayout`
 * its layout switcher for the same reason one layer down, and
 * `setSettingsCategory` opens one category of the settings page, which shows one
 * at a time, and `setPanel` opens the top bar's ⋮ panel, which is where the bar's
 * working controls go when the window is too narrow to hold them on one line. A
 * step that rings a button in there says so with `panel: true`. `onFinish` is
 * called once, with how far the person got, which is what the caller stores.
 */
export function start({
  state,
  mode = 'advanced',
  setRoute,
  setLayout,
  setSettingsCategory,
  setPanel,
  layout = '',
  onFinish,
} = {}) {
  let topic = '';
  let steps = activeSteps(state, mode, topic);
  if (!steps.length) {
    onFinish?.(TOUR_VERSION);
    return;
  }

  const layer = document.createElement('div');
  layer.className = 'tour-layer';
  layer.setAttribute('role', 'dialog');
  layer.setAttribute('aria-modal', 'true');
  layer.setAttribute('aria-label', t('tour.aria'));

  const block = document.createElement('div');
  block.className = 'tour-block';
  const ring = document.createElement('div');
  ring.className = 'tour-ring';
  const card = document.createElement('div');
  card.className = 'tour-card';
  card.tabIndex = -1;
  card.innerHTML = `
    <p class="tour-count"></p>
    <h3 class="tour-title"></h3>
    <p class="tour-body"></p>
    <div class="tour-choice" hidden></div>
    <div class="tour-nav">
      <button type="button" class="btn btn-ghost" data-tour="skip">${t('tour.skip')}</button>
      <span class="tour-spacer"></span>
      <button type="button" class="btn" data-tour="back">${t('tour.back')}</button>
      <button type="button" class="btn btn-primary" data-tour="next">${t('tour.next')}</button>
    </div>`;
  layer.append(block, ring, card);

  let index = 0;
  let finished = false;
  // The view the tour has left the person on. Every step without a route of its
  // own is about the dashboard — that is what all of them point at — so the tour
  // switches back to it. That is what makes "start the tour again" from Settings
  // work at all: the tour is usually begun there, and without this the steps that
  // take a sensor apart would explain a screen nobody is looking at.
  let showing = state.route || 'dashboard';
  // The layout the tour has left the person on, for the same reason as the view:
  // eight of the steps take a card apart, and a person who saved the wall or the
  // tree would otherwise be shown a ring around nothing at all. It starts at
  // whatever they had, so a step that does not care does not change it.
  let showingLayout = layout;
  // The settings category the tour has left the person on. Settings shows one
  // category at a time, so a step that rings a panel has to open its category
  // first — and unlike the view and the layout, two settings steps in a row do
  // not change the route, so this cannot be folded into `showing`. It starts
  // empty and is only ever set by a step that names one.
  let showingCategory = '';
  // Every draw is asynchronous — a view switch, then a wait for the target — so
  // each one carries a ticket and a stale draw drops its work on the floor.
  let ticket = 0;

  // A step's words are normally one key in the catalogue. Two of them have to say
  // something true about whether the sensors on screen were invented, and that is
  // only known for certain when the card is drawn — so a key may also be a function
  // of the state. The tour is restartable from Settings, and by then the sample
  // data is usually long gone.
  const phrase = (key) => (typeof key === 'function' ? key({ state }) : key);

  // Same idea for a step's target, which may depend on the state in exactly the
  // same way: the card the tour takes apart is not always the same card.
  const where = (target) => (typeof target === 'function' ? target({ state }) : target);

  const paint = (step, el) => {
    card.querySelector('.tour-count').textContent = `${index + 1} / ${steps.length}`;
    card.querySelector('.tour-title').textContent = t(phrase(step.titleKey));
    card.querySelector('.tour-body').textContent = t(phrase(step.bodyKey));
    card.querySelector('[data-tour="back"]').disabled = index === 0;
    card.querySelector('[data-tour="next"]').textContent =
      index === steps.length - 1 ? t('tour.finish') : t('tour.next');

    const choices = card.querySelector('.tour-choice');
    choices.hidden = !step.choice;
    if (step.choice) {
      choices.innerHTML = step.choice.map((id) =>
        `<button type="button" class="btn" data-tour-topic="${id}">${t(`tour.choose.${id}`)}</button>`).join('');
    }

    if (el) {
      bringIntoView(el);
      const at = el.getBoundingClientRect();
      const pad = 6;
      ring.hidden = false;
      ring.style.left = `${at.left - pad}px`;
      ring.style.top = `${at.top - pad}px`;
      ring.style.width = `${at.width + pad * 2}px`;
      ring.style.height = `${at.height + pad * 2}px`;
    } else {
      ring.hidden = true;
    }
    place(card, el ? ring : null, step.placement);
  };

  const draw = async () => {
    if (finished) return;
    const step = steps[index];
    if (!step) return finish(TOUR_VERSION);
    const mine = ++ticket;
    // Show the words at once and move the ring when it can be placed: a step on
    // another view is legible while that view is still loading.
    paint(step, null);
    // Only switch when the view actually changes: setRoute redraws the whole view,
    // and redrawing the dashboard would throw away the cards that the previous
    // step was ringed on and that this one may be about too.
    const route = step.route || 'dashboard';
    if (route !== showing) {
      showing = route;
      await setRoute?.(route);
    }
    // Same rule one layer down. This is the repair of the one real fragility the
    // layout switcher introduced: a step that rings a card has to be looking at
    // cards.
    if (step.layout && step.layout !== showingLayout) {
      showingLayout = step.layout;
      await setLayout?.(step.layout);
    }
    // And one layer down again, for the two settings steps: the category has to
    // be open before the target is looked for, or `waitFor` waits out its whole
    // budget for a panel that is in the document but hidden. This runs even when
    // the route did not change — two settings steps in a row never change it —
    // and only for a step that names a category, so the dashboard steps leave
    // the settings page wherever the person left it.
    if (step.category && step.category !== showingCategory) {
      showingCategory = step.category;
      await setSettingsCategory?.(step.category);
    }
    // And the same again for the ⋮: on a narrow window the top bar folds its four
    // working controls into a panel, and a step that rings one of them needs that
    // panel open before the target is looked for. It is asserted on every step
    // rather than changed on a difference, because the panel also closes itself on
    // a press anywhere else — the tour's own Next button, say — and a step must not
    // depend on what the last one happened to leave behind. On a wide window there
    // is no panel and both calls are a no-op.
    setPanel?.(!!step.panel);
    const el = step.target ? await waitFor(where(step.target)) : null;
    if (mine !== ticket || finished) return;
    paint(step, el);
    card.focus();
  };

  const finish = (reached) => {
    if (finished) return;
    finished = true;
    layer.remove();
    document.removeEventListener('keydown', onKey, true);
    window.removeEventListener('resize', redraw);
    onFinish?.(reached);
  };

  const go = (delta) => {
    const next = index + delta;
    if (next < 0) return;
    if (next >= steps.length) return finish(TOUR_VERSION);
    index = next;
    void draw();
  };

  const choose = (picked) => {
    topic = picked;
    // The prefix of the list is unchanged — the choice sits before every
    // topical step — so the index still points at this card.
    steps = activeSteps(state, mode, topic);
    go(1);
  };

  const redraw = () => {
    const step = steps[index];
    // `where`, not the raw target: a step's target may be a function of the
    // state, and `querySelector` fed a function throws instead of missing.
    const target = step ? where(step.target) : null;
    if (step) paint(step, target ? document.querySelector(target) : null);
  };

  card.addEventListener('click', (event) => {
    const pick = event.target.closest('[data-tour-topic]');
    if (pick) return choose(pick.dataset.tourTopic);
    const button = event.target.closest('[data-tour]');
    if (!button) return;
    if (button.dataset.tour === 'skip') finish(TOUR_VERSION);
    else if (button.dataset.tour === 'back') go(-1);
    else go(1);
  });

  function onKey(event) {
    if (event.key === 'Escape') finish(TOUR_VERSION);
    else if (event.key === 'ArrowRight') go(1);
    else if (event.key === 'ArrowLeft') go(-1);
    else return;
    event.preventDefault();
  }

  document.addEventListener('keydown', onKey, true);
  window.addEventListener('resize', redraw);

  document.body.append(layer);
  void draw();
  card.focus();
}
