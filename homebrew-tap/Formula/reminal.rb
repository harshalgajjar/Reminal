class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.10.4"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.4/reminal_0.10.4_darwin_arm64.tar.gz"
      sha256 "a919bb39c344b0c7df67ce11c25441b96b57bf228293c5f227f6f4affb00b6b6"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.4/reminal_0.10.4_darwin_amd64.tar.gz"
      sha256 "28faf92d21be7ce93779800a2572a537f69b82fc2e3568db06ec21492871a58e"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.4/reminal_0.10.4_linux_arm64.tar.gz"
      sha256 "80a53e5b93160867c218a767526675cf03907cec2db49b5820d1bcb9ee23c251"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.4/reminal_0.10.4_linux_amd64.tar.gz"
      sha256 "a2716127a3160247c0d168170af586dcc4a847c4c3029986e8514bab14f67a1a"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
