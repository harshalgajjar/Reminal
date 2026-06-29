class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.10.1"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.1/reminal_0.10.1_darwin_arm64.tar.gz"
      sha256 "5ed02c4b109a09576f22469e1db64d7f66342cbde235f7bc85740763b3776977"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.1/reminal_0.10.1_darwin_amd64.tar.gz"
      sha256 "cdb7024f3285e88ba8ca84108aa0426b6a3597cbede10274645aca2c5f8243ec"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.1/reminal_0.10.1_linux_arm64.tar.gz"
      sha256 "cc0cdc48c1a4941ac215ba0b9ec8e7ac6e6739ed63f585b4287843f2cb1b440b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.1/reminal_0.10.1_linux_amd64.tar.gz"
      sha256 "8631826a8a18260bfadd54347f40de1973070c275b318aa9894b15e952be18b5"
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
