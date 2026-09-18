package handlers

import (
	"strings"
	"sync"
	"testing"

	"bro-bot/internal/adapters/mock"
)

// TestActiveAgentSnapshotIsConsistent — регрессия на гонку вокруг активного агента.
//
// Раньше имя активного агента и его адаптер были двумя незащищёнными глобалями: команда /agent
// меняла их по очереди, а фоновые горутины пайплайна задач читали — детектор гонок падал на
// TestNewCommandWithAgent по стечению таймингов. Здесь переключение и чтение идут в тесной петле,
// поэтому под `-race` возврат к незащищённым переменным виден сразу и не зависит от везения.
//
// Проверка совпадения имени и адаптера — дополнительная страховка инварианта «пара всегда
// описывает одного агента». Сама по себе она вероятностная: узкое окно между двумя чтениями
// она поймать не обязана, поэтому основной сторож здесь именно детектор гонок.
func TestActiveAgentSnapshotIsConsistent(t *testing.T) {
	setupTestApp(t)

	prevFramework, prevName := ActiveAgent()
	t.Cleanup(func() { SetActiveAgent(prevFramework, prevName) })

	agyMock := &mock.AgentFramework{Name: "agy"}
	claudeMock := &mock.AgentFramework{Name: "claude"}

	const iterations = 200

	var (
		wg       sync.WaitGroup
		mismatch = make(chan string, 8)
		stop     = make(chan struct{})
	)

	// Писатель: переключает агента так же, как это делают /agent, /mode и /new <агент>.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				SetActiveAgent(agyMock, "agy")
			} else {
				SetActiveAgent(claudeMock, "claude")
			}
		}
	}()

	// Читатели: имитируют фоновые горутины задач и диалога.
	for _, requested := range []string{"agy", "claude"} {
		wg.Add(1)
		go func(requested string) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}

				framework, err := agentFrameworkFor(requested)
				if err != nil {
					continue
				}
				// Мок возвращается только когда запрошенный агент совпал с активным,
				// поэтому его имя обязано совпадать с запрошенным.
				if m, ok := framework.(*mock.AgentFramework); ok && !strings.EqualFold(m.Name, requested) {
					select {
					case mismatch <- "запрошен " + requested + ", получен адаптер " + m.Name:
					default:
					}
					return
				}
			}
		}(requested)
	}

	// Наблюдатель за парой: имя и адаптер всегда описывают одного агента.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}

			framework, name := ActiveAgent()
			if m, ok := framework.(*mock.AgentFramework); ok && !strings.EqualFold(m.Name, name) {
				select {
				case mismatch <- "пара рассогласована: имя " + name + ", адаптер " + m.Name:
				default:
				}
				return
			}
		}
	}()

	wg.Wait()
	close(mismatch)

	for msg := range mismatch {
		t.Error(msg)
	}
}

// TestSetActiveAgentNormalizesName — пустое имя не должно обнулять активного агента.
func TestSetActiveAgentNormalizesName(t *testing.T) {
	setupTestApp(t)

	prevFramework, prevName := ActiveAgent()
	t.Cleanup(func() { SetActiveAgent(prevFramework, prevName) })

	agent := &mock.AgentFramework{Name: "agy"}

	SetActiveAgent(agent, "  CLAUDE  ")
	if got := ActiveAgentName(); got != "claude" {
		t.Errorf("имя приведено к %q, ожидали claude", got)
	}

	SetActiveAgent(agent, "")
	if got := ActiveAgentName(); got != "agy" {
		t.Errorf("пустое имя должно превращаться в agy, получили %q", got)
	}

	// Пустой адаптер допустим: agentFrameworkFor в этом случае собирает его заново.
	SetActiveAgent(nil, "agy")
	if framework, name := ActiveAgent(); framework != nil || name != "agy" {
		t.Errorf("ожидали пустой адаптер и имя agy, получили %v / %q", framework, name)
	}
}
